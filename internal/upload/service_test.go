package upload

import (
	"strings"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

func TestCategoryOf(t *testing.T) {
	tests := []struct {
		mime string
		name string
		want store.FileCategory
	}{
		{"image/png", "a.png", store.CategoryImage},
		{"image/svg+xml", "a.svg", store.CategoryImage},
		{"video/mp4", "a.mp4", store.CategoryVideo},
		{"audio/mpeg", "a.mp3", store.CategoryAudio},
		{"application/pdf", "a.pdf", store.CategoryDoc},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "a.docx", store.CategoryDoc},
		{"application/zip", "a.zip", store.CategoryArchive},
		{"application/octet-stream", "a.heic", store.CategoryImage},
		{"application/octet-stream", "a.bin", store.CategoryOther},
		{"", "photo.jpg", store.CategoryImage},
		{"text/plain; charset=utf-8", "note.txt", store.CategoryDoc},
	}
	for _, tt := range tests {
		t.Run(tt.mime+"/"+tt.name, func(t *testing.T) {
			if got := CategoryOf(tt.mime, tt.name); got != tt.want {
				t.Fatalf("CategoryOf(%q,%q) = %q, want %q", tt.mime, tt.name, got, tt.want)
			}
		})
	}
}

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"普通键", "202611/a.png", "202611/a.png", false},
		{"清理点段", "a/./b/../c.png", "a/b/c.png", false},
		{"反斜杠归一", `a\b.png`, "a/b.png", false},
		{"绝对路径", "/etc/passwd", "", true},
		{"含冒号键", "C:/secret", "C:/secret", false},
		{"含协议", "https://evil/x", "", true},
		{"空", "", "", true},
		{"控制字符", "a/\x00b", "", true},
		{"过长", strings.Repeat("a", 600), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeKey(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("SanitizeKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateBucketName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"default", true}, {"my-bucket_1", true}, {"a.b", true},
		{"A", false}, {"UPPER", false}, {"a b", false},
		{"../escape", false}, {"", false}, {strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		if err := ValidateBucketName(tt.name); (err == nil) != tt.want {
			t.Errorf("ValidateBucketName(%q) err = %v, want valid=%v", tt.name, err, tt.want)
		}
	}
}

func TestSafeBase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a.png", "a.png"},
		{"../../etc/passwd", "passwd"},
		{"", "file"},
		{"a\x00b.png", "a_b.png"},
	}
	for _, tt := range tests {
		if got := safeBase(tt.in); got != tt.want {
			t.Errorf("safeBase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
