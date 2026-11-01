//go:build unit

package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldSkipAvatarAdoptionValidationErrors(t *testing.T) {
	for _, err := range []error{
		service.ErrAvatarInvalid,
		service.ErrAvatarTooLarge,
		service.ErrAvatarNotImage,
		service.ErrAvatarDimensionsTooLarge,
		service.ErrAvatarWebPTooLarge,
		service.ErrAvatarTooComplex,
	} {
		require.True(t, shouldSkipAvatarAdoption(err), "%v", err)
		require.True(t, shouldSkipAvatarAdoption(fmt.Errorf("validate avatar: %w", err)), "%v", err)
	}
	require.False(t, shouldSkipAvatarAdoption(nil))
	require.False(t, shouldSkipAvatarAdoption(service.ErrUserNotFound))
}

func TestUserHandlerUpdateProfileRejectsAvatarBomb(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	header := encoded.Bytes()[:33]
	binary.BigEndian.PutUint32(header[16:20], 4097)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))

	for _, size := range []int{len(header), 20*1024 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := make([]byte, size)
			copy(data, header)
			avatar := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
			original := service.User{
				ID:           11,
				Email:        "avatar@example.com",
				Username:     "unchanged",
				Role:         service.RoleUser,
				Status:       service.StatusActive,
				AvatarURL:    "https://example.com/old.png",
				AvatarSource: "remote_url",
			}
			user := original
			repo := &userHandlerRepoStub{user: &user}
			handler := NewUserHandler(service.NewUserService(repo, nil, nil, nil), nil, nil, nil, nil, nil)
			body, err := json.Marshal(map[string]string{"avatar_url": avatar, "username": "should-not-be-saved"})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/user", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 11})

			handler.UpdateProfile(c)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			var result response.Response
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Equal(t, "AVATAR_DIMENSIONS_TOO_LARGE", result.Reason)
			require.Equal(t, original, *repo.user)
		})
	}
}
