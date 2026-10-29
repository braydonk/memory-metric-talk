package bytefmt

import "testing"

func TestParseLimit(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		allowZero bool
		want      Limit
		wantStr   string
		wantErr   bool
	}{
		{name: "max lowercase", raw: "max", want: Limit{Unlimited: true}, wantStr: "max"},
		{name: "MAX uppercase padded", raw: "  MAX ", want: Limit{Unlimited: true}, wantStr: "max"},
		{name: "zero disallowed", raw: "0", allowZero: false, wantErr: true},
		{name: "zero allowed", raw: "0", allowZero: true, want: Limit{Bytes: 0}, wantStr: "0"},
		{name: "raw bytes", raw: "62914560", want: Limit{Bytes: 62914560}, wantStr: "62914560"},
		{name: "iec MiB", raw: "60MiB", want: Limit{Bytes: 60 * 1024 * 1024}, wantStr: "62914560"},
		{name: "short M", raw: "60M", want: Limit{Bytes: 60 * 1024 * 1024}, wantStr: "62914560"},
		{name: "si MB", raw: "80MB", want: Limit{Bytes: 80 * 1000 * 1000}, wantStr: "80000000"},
		{name: "fractional GiB", raw: "1.5GiB", want: Limit{Bytes: 1610612736}, wantStr: "1610612736"},
		{name: "empty", raw: "", wantErr: true},
		{name: "negative", raw: "-10MiB", wantErr: true},
		{name: "unknown unit", raw: "50foo", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLimit(tc.raw, tc.allowZero)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseLimit(%q) succeeded with %+v; want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLimit(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseLimit(%q) = %+v; want %+v", tc.raw, got, tc.want)
			}
			if got.String() != tc.wantStr {
				t.Fatalf("Limit.String() = %q; want %q", got.String(), tc.wantStr)
			}
		})
	}
}
