package v0_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	v0 "github.com/agentregistry-dev/agentregistry/internal/registry/api/handlers/v0"
	servicetesting "github.com/agentregistry-dev/agentregistry/internal/registry/service/testing"
	"github.com/agentregistry-dev/agentregistry/pkg/registry/database"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteSkillVersion_Success(t *testing.T) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Test API", "1.0.0"))
	fake := servicetesting.NewFakeRegistry()

	var gotName, gotVersion string
	fake.DeleteSkillFn = func(_ context.Context, skillName, version string) error {
		gotName = skillName
		gotVersion = version
		return nil
	}

	v0.RegisterSkillsEndpoints(api, "/v0", fake)

	req := httptest.NewRequest(http.MethodDelete, "/v0/skills/vue/versions/v0.0.1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "vue", gotName)
	assert.Equal(t, "v0.0.1", gotVersion)
	assert.Contains(t, w.Body.String(), `"message":"Skill deleted successfully"`)
}

func TestDeleteSkillVersion_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Test API", "1.0.0"))
	fake := servicetesting.NewFakeRegistry()

	fake.DeleteSkillFn = func(_ context.Context, skillName, version string) error {
		return database.ErrNotFound
	}

	v0.RegisterSkillsEndpoints(api, "/v0", fake)

	req := httptest.NewRequest(http.MethodDelete, "/v0/skills/vue/versions/v0.0.1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Skill not found")
}
