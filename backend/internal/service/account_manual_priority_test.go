package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManualPriorityProtectionPersistsAcrossAccountEdits(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "protected", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"}, Priority: 7,
		SkipDefaultGroupBind: true,
		Extra:                map[string]any{ManualPriorityProtectedExtraKey: true},
	})
	require.NoError(t, err)
	require.True(t, account.IsManualPriorityProtected())

	priority := 9
	account, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{
		Priority: &priority, Extra: map[string]any{"unrelated": true},
	})
	require.NoError(t, err)
	require.Equal(t, 9, account.Priority)
	require.True(t, account.IsManualPriorityProtected())

	account, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{
		Extra: map[string]any{ManualPriorityProtectedExtraKey: false},
	})
	require.NoError(t, err)
	require.False(t, account.IsManualPriorityProtected())
}

func TestManualPriorityProtectionRejectsNonBoolean(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "invalid", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		SkipDefaultGroupBind: true,
		Extra:                map[string]any{ManualPriorityProtectedExtraKey: "true"},
	})
	require.Error(t, err)
	require.Empty(t, repo.accounts)
}
