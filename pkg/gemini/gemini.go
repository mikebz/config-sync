package gemini

import (
	"context"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
	"kpt.dev/configsync/pkg/bugreport"
)

// Client has all the values we need to deal with gemini,
// in addition to what the regular genai.Client already provides.
type Client struct {
	apiKey string
	model  string
	client *genai.Client
}

// NewClient should be used instead of creating the struct directly.
func NewClient(ctx context.Context, apiKey, model string) (*Client, error) {
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, err
	}

	return &Client{apiKey: apiKey, model: model, client: client}, nil
}

// Ask function sends a prompt to gemini using the files that are provided for context.
func (c *Client) Ask(ctx context.Context, question string, readables []bugreport.Readable) (string, error) {

	parts := make([]genai.Part, 0, len(readables)+1)
	parts = append(parts, genai.Text(question))

	// upload all the files, but delete them at the end, just in case in the future
	// the function is called repeatedly with a more persistent client.
	for _, r := range readables {
		opts := &genai.UploadFileOptions{}
		opts.DisplayName = r.Name
		opts.MIMEType = "text/plain"

		file, err := c.client.UploadFile(ctx, "", r, opts)
		if err != nil {
			return "", err
		}

		parts = append(parts, genai.FileData{
			MIMEType: file.MIMEType,
			URI:      file.URI,
		})

		// we do not want these files to persist beyond one call
		defer func() {
			_ = c.client.DeleteFile(ctx, file.Name)
		}()
	}

	model := c.client.GenerativeModel(c.model)
	r, err := model.GenerateContent(ctx, parts...)

	if err != nil {
		return "", err
	}

	if len(r.Candidates) == 0 {
		return "I am at a loss of words...", nil
	}

	// collect the text result and return it as one readable string.
	result := ""
	for _, part := range r.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			result += string(txt)
		}
	}

	return result, nil

}
