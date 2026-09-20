package librarian

import "testing"

func TestParseSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    string
		want    Spec
		wantErr bool
	}{
		{
			name: "the shape this repository's manifest uses",
			spec: "hmd-inf-neptune@0.3.36:build",
			want: Spec{Name: "hmd-inf-neptune", Version: "0.3.36", ItemType: "build"},
		},
		{
			name: "an item type other than build",
			spec: "hmd-lang-foo@0.3:schema",
			want: Spec{Name: "hmd-lang-foo", Version: "0.3", ItemType: "schema"},
		},
		{
			name: "the optional file part is kept, not truncated",
			spec: "hmd-lang-foo@0.3:schema:entities/thing.json",
			want: Spec{Name: "hmd-lang-foo", Version: "0.3", ItemType: "schema", FilePart: "entities/thing.json"},
		},
		{"no version", "hmd-vpc:build", Spec{}, true},
		{"no item type", "hmd-vpc@0.2.41", Spec{}, true},
		{"empty item type", "hmd-vpc@0.2.41:", Spec{}, true},
		{"empty version", "hmd-vpc@:build", Spec{}, true},
		{"no name", "@0.2.41:build", Spec{}, true},
		{"empty", "", Spec{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSpec(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSpec(%q) = %+v, want an error", tt.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSpec(%q): %v", tt.spec, err)
			}
			if got != tt.want {
				t.Errorf("ParseSpec(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

// TestContentPath pins the address against content_item_path_from_parts. The
// two halves of the grammar have to agree or a pin resolves to a 404 that
// looks like an unpublished version.
func TestContentPath(t *testing.T) {
	t.Parallel()

	spec, err := ParseSpec("hmd-inf-neptune@0.3.36:build")
	if err != nil {
		t.Fatal(err)
	}
	want := "repository:/hmd-inf-neptune/0.3.36/hmd-inf-neptune_0.3.36_build.zip"
	if got := spec.ContentPath(); got != want {
		t.Errorf("ContentPath() = %q, want %q", got, want)
	}
}
