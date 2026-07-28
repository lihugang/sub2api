//go:build unit

package repository

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserEntityToServicePreservesModelRoutingNoticeMode(t *testing.T) {
	user := userEntityToService(&dbent.User{
		ModelRoutingNoticeMode: service.ModelRoutingNoticeModeDisabled,
	})

	require.Equal(t, service.ModelRoutingNoticeModeDisabled, user.ModelRoutingNoticeMode)
}
