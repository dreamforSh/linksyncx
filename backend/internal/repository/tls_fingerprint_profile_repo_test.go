package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/model"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newTLSFingerprintProfileRepoSQLite(t *testing.T) service.TLSFingerprintProfileRepository {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return NewTLSFingerprintProfileRepository(client)
}

func requireTLSProfileRepoAppError(t *testing.T, err error, code int, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, infraerrors.Code(err), err.Error())
	require.Equal(t, reason, infraerrors.Reason(err))
}

// 模板名称在表上唯一。重名创建、改名撞上已有模板必须翻译成 409 TLS_PROFILE_NAME_EXISTS，
// 更新、删除已经不存在的模板翻译成 404 TLS_PROFILE_NOT_FOUND：原样的 ent 约束错误 / NotFoundError
// 在 handler 里会落到 infraerrors 的兜底，管理员只看到 500 "internal error"，也无从本地化。
func TestTLSFingerprintProfileRepositoryTranslatesNameConflictAndMissingRows(t *testing.T) {
	repo := newTLSFingerprintProfileRepoSQLite(t)
	ctx := context.Background()

	node, err := repo.Create(ctx, &model.TLSFingerprintProfile{Name: "node", Curves: []uint16{29}, KeyShareGroups: []uint16{29}})
	require.NoError(t, err)
	chrome, err := repo.Create(ctx, &model.TLSFingerprintProfile{Name: "chrome"})
	require.NoError(t, err)

	_, err = repo.Create(ctx, &model.TLSFingerprintProfile{Name: "node"})
	requireTLSProfileRepoAppError(t, err, http.StatusConflict, "TLS_PROFILE_NAME_EXISTS")

	renamed := *chrome
	renamed.Name = "node"
	_, err = repo.Update(ctx, &renamed)
	requireTLSProfileRepoAppError(t, err, http.StatusConflict, "TLS_PROFILE_NAME_EXISTS")

	ghost := *node
	ghost.ID = node.ID + chrome.ID + 1000
	ghost.Name = "ghost"
	_, err = repo.Update(ctx, &ghost)
	requireTLSProfileRepoAppError(t, err, http.StatusNotFound, "TLS_PROFILE_NOT_FOUND")

	require.NoError(t, repo.Delete(ctx, node.ID))
	requireTLSProfileRepoAppError(t, repo.Delete(ctx, node.ID), http.StatusNotFound, "TLS_PROFILE_NOT_FOUND")

	// GetByID 的约定不变：找不到返回 (nil, nil)，由 handler 回 404。
	got, err := repo.GetByID(ctx, node.ID)
	require.NoError(t, err)
	require.Nil(t, got)

	// 释放出来的名称可以再用，未被改名的模板保持原样。
	_, err = repo.Create(ctx, &model.TLSFingerprintProfile{Name: "node"})
	require.NoError(t, err)
	stored, err := repo.GetByID(ctx, chrome.ID)
	require.NoError(t, err)
	require.Equal(t, "chrome", stored.Name)
}
