package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageGen_BuildWorkflow(t *testing.T) {
	t.Parallel()

	workflow := buildWorkflow("a cat in space", "blurry", 512, 768, 30, 42, "test-model.safetensors")

	// Verify the workflow can be marshaled to JSON.
	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatalf("failed to marshal workflow: %v", err)
	}

	s := string(data)

	// Check prompt is in the workflow.
	if !strings.Contains(s, "a cat in space") {
		t.Error("expected prompt in workflow")
	}
	if !strings.Contains(s, "blurry") {
		t.Error("expected negative prompt in workflow")
	}

	// Check dimensions are in the workflow.
	if !strings.Contains(s, `"width":512`) {
		t.Error("expected width 512 in workflow")
	}
	if !strings.Contains(s, `"height":768`) {
		t.Error("expected height 768 in workflow")
	}

	// Check steps and seed.
	if !strings.Contains(s, `"steps":30`) {
		t.Error("expected steps 30 in workflow")
	}
	if !strings.Contains(s, `"seed":42`) {
		t.Error("expected seed 42 in workflow")
	}

	// Check node structure.
	if !strings.Contains(s, "KSampler") {
		t.Error("expected KSampler node")
	}
	if !strings.Contains(s, "CheckpointLoaderSimple") {
		t.Error("expected CheckpointLoaderSimple node")
	}
	if !strings.Contains(s, "SaveImage") {
		t.Error("expected SaveImage node")
	}

	// Check checkpoint name.
	if !strings.Contains(s, "test-model.safetensors") {
		t.Error("expected checkpoint name in workflow")
	}
}

func TestImageGen_SubmitAndPoll(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	promptID := "test-prompt-123"
	pollCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"` + promptID + `"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/history/"+promptID:
			pollCount++
			if pollCount < 2 {
				// Not ready yet — return empty.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
				return
			}
			// Ready — return output.
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				promptID: map[string]any{
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "murmur_00001_.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			}
			data, _ := json.Marshal(resp)
			_, _ = w.Write(data)

		case r.Method == http.MethodGet && r.URL.Path == "/view":
			// Return a fake PNG (just some bytes).
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("fake-png-data"))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: srv.URL,
		OutputDir:   outputDir,
		HTTPClient:  srv.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"prompt": "a beautiful sunset",
		"width":  float64(512),
		"height": float64(512),
		"steps":  float64(10),
		"seed":   float64(42),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Image generated") {
		t.Errorf("expected 'Image generated' in result, got: %s", result)
	}
	if !strings.Contains(result, "512x512") {
		t.Errorf("expected dimensions in result, got: %s", result)
	}

	// Verify file was saved.
	files, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file in output dir, got %d", len(files))
	}

	// Verify file content.
	data, err := os.ReadFile(filepath.Join(outputDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-png-data" {
		t.Errorf("unexpected file content: %s", string(data))
	}
}

func TestImageGen_SubmitAndPollWithUpload(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	promptID := "upload-test-456"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"` + promptID + `"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/history/"+promptID:
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				promptID: map[string]any{
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{"filename": "out.png", "subfolder": "", "type": "output"},
							},
						},
					},
				},
			}
			data, _ := json.Marshal(resp)
			_, _ = w.Write(data)

		case r.Method == http.MethodGet && r.URL.Path == "/view":
			_, _ = w.Write([]byte("png-bytes"))

		case r.Method == http.MethodPost && r.URL.Path == "/upload":
			_, _ = w.Write([]byte("https://cdn.example.com/image.png"))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: srv.URL,
		OutputDir:   outputDir,
		UploadURL:   srv.URL + "/upload",
		HTTPClient:  srv.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"prompt": "test upload",
		"seed":   float64(1),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "https://cdn.example.com/image.png") {
		t.Errorf("expected upload URL in result, got: %s", result)
	}
}

func TestImageGen_InvalidDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		width   float64
		height  float64
		errText string
	}{
		{name: "width too small", width: 32, height: 512, errText: "width must be between"},
		{name: "width too large", width: 4096, height: 512, errText: "width must be between"},
		{name: "height too small", width: 512, height: 32, errText: "height must be between"},
		{name: "height too large", width: 512, height: 4096, errText: "height must be between"},
		{name: "width not divisible by 8", width: 513, height: 512, errText: "divisible by 8"},
		{name: "height not divisible by 8", width: 512, height: 513, errText: "divisible by 8"},
	}

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: "http://localhost:8188",
		OutputDir:   t.TempDir(),
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"prompt": "test",
				"width":  tt.width,
				"height": tt.height,
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("expected error containing %q, got: %v", tt.errText, err)
			}
		})
	}
}

func TestImageGen_InvalidSteps(t *testing.T) {
	t.Parallel()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: "http://localhost:8188",
		OutputDir:   t.TempDir(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"prompt": "test",
		"steps":  float64(200),
	})
	if err == nil {
		t.Fatal("expected error for steps > 100")
	}
	if !strings.Contains(err.Error(), "steps must be between") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImageGen_RequiredPrompt(t *testing.T) {
	t.Parallel()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: "http://localhost:8188",
		OutputDir:   t.TempDir(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImageGen_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"timeout-test"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/history/timeout-test":
			// Never return results — always empty.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: srv.URL,
		OutputDir:   t.TempDir(),
		HTTPClient:  srv.Client(),
	})

	// Use a very short context timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := tool.Handler(ctx, map[string]any{
		"prompt": "test timeout",
		"seed":   float64(1),
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestImageGen_SubmitError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: srv.URL,
		OutputDir:   t.TempDir(),
		HTTPClient:  srv.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"prompt": "test error",
		"seed":   float64(1),
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got: %v", err)
	}
}

func TestValidateDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		width   int
		height  int
		wantErr bool
	}{
		{name: "valid 1024x1024", width: 1024, height: 1024, wantErr: false},
		{name: "valid 512x768", width: 512, height: 768, wantErr: false},
		{name: "valid min 64x64", width: 64, height: 64, wantErr: false},
		{name: "valid max 2048x2048", width: 2048, height: 2048, wantErr: false},
		{name: "width too small", width: 32, height: 512, wantErr: true},
		{name: "height too small", width: 512, height: 32, wantErr: true},
		{name: "width too large", width: 2056, height: 512, wantErr: true},
		{name: "height too large", width: 512, height: 2056, wantErr: true},
		{name: "width not div 8", width: 513, height: 512, wantErr: true},
		{name: "height not div 8", width: 512, height: 513, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDimensions(tt.width, tt.height)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSanitizeFilenameComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "clean id", input: "abc-123", want: "abc-123"},
		{name: "path traversal", input: "../../../etc/passwd", want: "______etc_passwd"},
		{name: "slashes", input: "foo/bar\\baz", want: "foo_bar_baz"},
		{name: "special chars", input: "a&b=c?d", want: "a_b_c_d"},
		{name: "empty", input: "", want: "unknown"},
		{name: "dots only", input: "..", want: "_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeFilenameComponent(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilenameComponent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestImageGen_SeedZero(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	promptID := "seed-zero-test"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"` + promptID + `"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/history/"+promptID:
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				promptID: map[string]any{
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{"filename": "out.png", "subfolder": "", "type": "output"},
							},
						},
					},
				},
			}
			data, _ := json.Marshal(resp)
			_, _ = w.Write(data)

		case r.Method == http.MethodGet && r.URL.Path == "/view":
			_, _ = w.Write([]byte("png-data"))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tool := NewImageGenTool(ImageGenToolConfig{
		ComfyUIHost: srv.URL,
		OutputDir:   outputDir,
		HTTPClient:  srv.Client(),
	})

	// Explicitly pass seed=0 — should be used as-is, not replaced with random.
	result, err := tool.Handler(context.Background(), map[string]any{
		"prompt": "test seed zero",
		"seed":   float64(0),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "seed 0") {
		t.Errorf("expected seed 0 in result, got: %s", result)
	}
}

func TestExtractOutputInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     any
		wantFile string
		wantErr  bool
	}{
		{
			name: "valid output",
			data: map[string]any{
				"outputs": map[string]any{
					"9": map[string]any{
						"images": []any{
							map[string]any{"filename": "test.png", "subfolder": "sub"},
						},
					},
				},
			},
			wantFile: "test.png",
		},
		{
			name:    "no outputs",
			data:    map[string]any{},
			wantErr: true,
		},
		{
			name: "empty images",
			data: map[string]any{
				"outputs": map[string]any{
					"9": map[string]any{
						"images": []any{},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := extractOutputInfo(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Filename != tt.wantFile {
				t.Errorf("expected filename %q, got %q", tt.wantFile, info.Filename)
			}
		})
	}
}
