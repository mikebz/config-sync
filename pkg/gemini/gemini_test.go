package gemini

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"kpt.dev/configsync/pkg/bugreport"
)

// test creation of the gemini test client
func TestAsk(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	ctx := context.Background()
	client, err := NewClient(ctx, apiKey, "gemini-2.0-flash")
	assert.NoError(t, err)
	assert.NotNil(t, client)

	response, err := client.Ask(ctx, "What does Config Sync do for a GKE cluster?",
		[]bugreport.Readable{})

	assert.NoError(t, err)
	assert.NotEmpty(t, response)
}

// test creation of the gemini test client
func TestAskWithFiles(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	ctx := context.Background()
	client, err := NewClient(ctx, apiKey, "gemini-2.0-flash")
	assert.NoError(t, err)
	assert.NotNil(t, client)

	statusFile, err := os.Open("testdata/bugreport1/status.txt")
	assert.NoError(t, err)
	assert.NotNil(t, statusFile)

	versionFile, err := os.Open("testdata/bugreport1/version.txt")
	assert.NoError(t, err)
	assert.NotNil(t, versionFile)

	readables := []bugreport.Readable{
		{
			Name:       "status.txt",
			ReadCloser: statusFile,
		},
		{
			Name:       "version.txt",
			ReadCloser: versionFile,
		},
	}

	response, err := client.Ask(ctx, "Tell me how I fix the error in version.txt and status.txt",
		readables)

	t.Log(">>>" + response)

	assert.NoError(t, err)
	assert.NotEmpty(t, response)
}
