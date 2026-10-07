package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUpdateUpstreamBillingProbeSnapshotRequiresSameIdentityAndSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		affected int64
		wantErr  error
	}{
		{name: "same identity and snapshot", affected: 1},
		{name: "identity or snapshot changed", affected: 0, wantErr: service.ErrUpstreamBillingProbeIdentityChanged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			driver := entsql.OpenDB(dialect.Postgres, db)
			client := dbent.NewClient(dbent.Driver(driver))
			t.Cleanup(func() { _ = client.Close() })

			mock.ExpectBegin()
			tx, err := client.Tx(context.Background())
			require.NoError(t, err)
			mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR SHARE")).
				WithArgs(int64(9)).
				WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
					AddRow("http", "127.0.0.1", 3128, "user", "pass", service.StatusActive))
			mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta("WHERE id = $3")+`.*`+regexp.QuoteMeta("AND platform = $4")+`.*`+regexp.QuoteMeta("AND type = $5")+`.*`+regexp.QuoteMeta("AND credentials = $6::jsonb")+`.*`+regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $7")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $8::jsonb")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe_enabled', 'null'::jsonb) = $9::jsonb")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_rate_sync_enabled', 'null'::jsonb) = $10::jsonb")).
				WithArgs(sqlmock.AnyArg(), nil, int64(17), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test","base_url":"http://127.0.0.1:8080"}`, int64(9), `{"status":"stale"}`, "null", "null").
				WillReturnResult(sqlmock.NewResult(0, tt.affected))
			if tt.affected > 0 {
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
					WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(1, 1))
			}
			repo := newAccountRepositoryWithSQL(client, &recordingSQLExecutor{err: errors.New("must use transaction client")}, nil)
			proxyID := int64(9)
			account := &service.Account{
				ID:       17,
				Platform: service.PlatformOpenAI,
				Type:     service.AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":  "sk-test",
					"base_url": "http://127.0.0.1:8080",
				},
				ProxyID: &proxyID,
				Proxy: &service.Proxy{
					ID:       proxyID,
					Protocol: "http",
					Host:     "127.0.0.1",
					Port:     3128,
					Username: "user",
					Password: "pass",
					Status:   service.StatusActive,
				},
				Extra: map[string]any{
					service.UpstreamBillingProbeExtraKey: map[string]any{"status": "stale"},
				},
			}

			txCtx := dbent.NewTxContext(context.Background(), tx)
			err = repo.UpdateUpstreamBillingProbeSnapshot(txCtx, account, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK}, nil)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUpdateUpstreamBillingProbeSnapshotCommitsSnapshotAndOutboxAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta("rate_multiplier = CASE")+`.*`+regexp.QuoteMeta("THEN $2::numeric")+`.*`+regexp.QuoteMeta("AND credentials = $6::jsonb")+`.*`+regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $7")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $8::jsonb")).
		WithArgs(sqlmock.AnyArg(), 0.065, int64(17), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil, "null", "true", "true").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(client, db, nil)
	account := &service.Account{
		ID:          17,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey:    true,
			service.UpstreamBillingRateSyncEnabledExtraKey: true,
		},
	}
	rateMultiplier := 0.065

	err = repo.UpdateUpstreamBillingProbeSnapshot(
		context.Background(),
		account,
		&service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK},
		&rateMultiplier,
	)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUpstreamBillingProbeSnapshotRejectsChangedProxyIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR SHARE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "new.example", 3128, "user", "pass", service.StatusActive))

	proxyID := int64(9)
	account := &service.Account{
		ID:          17,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		ProxyID:     &proxyID,
		Proxy: &service.Proxy{
			ID: proxyID, Protocol: "http", Host: "old.example", Port: 3128,
			Username: "user", Password: "pass", Status: service.StatusActive,
		},
	}
	repo := newAccountRepositoryWithSQL(client, db, nil)
	err = repo.UpdateUpstreamBillingProbeSnapshot(dbent.NewTxContext(context.Background(), tx), account, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK}, nil)

	require.ErrorIs(t, err, service.ErrUpstreamBillingProbeIdentityChanged)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUpstreamBillingProbeChecksPriorityProtectionOnCurrentRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	priority, rate := 110, 0.9
	account := &service.Account{
		ID: 17, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey:    true,
			service.UpstreamBillingRateSyncEnabledExtraKey: true,
			service.ManualPriorityProtectedExtraKey:        false,
		},
	}
	// Even an unprotected loaded snapshot must produce a row-level check,
	// rather than an unconditional write, for in-flight administrator changes.
	guard := `priority = CASE WHEN COALESCE(extra @> '{"manual_priority_protected": true}'::jsonb, false) THEN priority ELSE $2 END`
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta(guard)).
		WithArgs(sqlmock.AnyArg(), priority, rate, int64(17), service.PlatformOpenAI, service.AccountTypeAPIKey,
			`{"api_key":"sk-test"}`, nil, "null", "true", "true").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(client, db, nil)
	err = repo.UpdateUpstreamBillingProbeSnapshot(context.Background(), account, &service.UpstreamBillingProbeSnapshot{
		Status: service.UpstreamBillingProbeStatusOK, AccountPriority: &priority,
	}, &rate)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUpstreamBillingProbeSnapshotRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $7")+`.*`+regexp.QuoteMeta("COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $8::jsonb")).
		WithArgs(sqlmock.AnyArg(), 0.7, int64(18), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"sk-test"}`, nil, "null", "true", "true").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(client, db, nil)
	account := &service.Account{
		ID:          18,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey:    true,
			service.UpstreamBillingRateSyncEnabledExtraKey: true,
		},
	}
	rateMultiplier := 0.7

	err = repo.UpdateUpstreamBillingProbeSnapshot(
		context.Background(),
		account,
		&service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK},
		&rateMultiplier,
	)

	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}
