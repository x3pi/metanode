package node

import (
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestKeybytesToHex(t *testing.T) {
	type args struct {
		str []byte
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		{
			name: "basic two-byte input",
			args: args{
				str: common.FromHex("1111"),
			},
			want: common.FromHex("0101010110"),
		},
		{
			name: "empty input",
			args: args{
				str: []byte{},
			},
			want: []byte{16}, // terminator only
		},
		{
			name: "single byte 0x00",
			args: args{
				str: []byte{0x00},
			},
			want: []byte{0x00, 0x00, 16},
		},
		{
			name: "single byte 0xff",
			args: args{
				str: []byte{0xff},
			},
			want: []byte{0x0f, 0x0f, 16},
		},
		{
			name: "single byte 0xab",
			args: args{
				str: []byte{0xab},
			},
			want: []byte{0x0a, 0x0b, 16},
		},
		{
			name: "multiple bytes",
			args: args{
				str: []byte{0x01, 0x23, 0x45},
			},
			want: []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 16},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeybytesToHex(tt.args.str); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("KeybytesToHex() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrefixLen(t *testing.T) {
	type args struct {
		a []byte
		b []byte
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "partial match",
			args: args{
				a: common.FromHex("01010302"),
				b: common.FromHex("01010202"),
			},
			want: 2,
		},
		{
			name: "no match",
			args: args{
				a: []byte{0x01, 0x02},
				b: []byte{0x03, 0x04},
			},
			want: 0,
		},
		{
			name: "full match same length",
			args: args{
				a: []byte{0x01, 0x02, 0x03},
				b: []byte{0x01, 0x02, 0x03},
			},
			want: 3,
		},
		{
			name: "full match different length",
			args: args{
				a: []byte{0x01, 0x02},
				b: []byte{0x01, 0x02, 0x03},
			},
			want: 2,
		},
		{
			name: "empty a",
			args: args{
				a: []byte{},
				b: []byte{0x01, 0x02},
			},
			want: 0,
		},
		{
			name: "both empty",
			args: args{
				a: []byte{},
				b: []byte{},
			},
			want: 0,
		},
		{
			name: "single element match",
			args: args{
				a: []byte{0x05},
				b: []byte{0x05, 0x06},
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrefixLen(tt.args.a, tt.args.b); got != tt.want {
				t.Errorf("PrefixLen() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHexToCompact(t *testing.T) {
	tests := []struct {
		name string
		hex  []byte
		want []byte
	}{
		{
			name: "even length without terminator",
			hex:  []byte{0x01, 0x02, 0x03, 0x04},
			want: []byte{0x00, 0x12, 0x34},
		},
		{
			name: "odd length without terminator",
			hex:  []byte{0x01, 0x02, 0x03},
			want: []byte{0x11, 0x23},
		},
		{
			name: "even length with terminator",
			hex:  []byte{0x01, 0x02, 0x03, 0x04, 16},
			want: []byte{0x20, 0x12, 0x34},
		},
		{
			name: "odd length with terminator",
			hex:  []byte{0x01, 0x02, 0x03, 16},
			want: []byte{0x31, 0x23},
		},
		{
			name: "empty without terminator",
			hex:  []byte{},
			want: []byte{0x00},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HexToCompact(tt.hex)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("HexToCompact() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCompactToHex(t *testing.T) {
	tests := []struct {
		name    string
		compact []byte
		want    []byte
	}{
		{
			name:    "empty input",
			compact: []byte{},
			want:    []byte{},
		},
		{
			name:    "even extension node",
			compact: []byte{0x00, 0x12, 0x34},
			want:    []byte{0x01, 0x02, 0x03, 0x04},
		},
		{
			name:    "odd extension node",
			compact: []byte{0x11, 0x23},
			want:    []byte{0x01, 0x02, 0x03},
		},
		{
			name:    "even leaf node (with term)",
			compact: []byte{0x20, 0x12, 0x34},
			want:    []byte{0x01, 0x02, 0x03, 0x04, 16},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompactToHex(tt.compact)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompactToHex() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHexToCompactRoundTrip(t *testing.T) {
	// HexToCompact -> CompactToHex should return original (even length, no terminator)
	original := []byte{0x0a, 0x0b, 0x0c, 0x0d}
	compact := HexToCompact(original)
	recovered := CompactToHex(compact)
	if !reflect.DeepEqual(original, recovered) {
		t.Errorf("round trip failed: original=%v recovered=%v", original, recovered)
	}
}

func TestHexToKeybytes(t *testing.T) {
	tests := []struct {
		name string
		hex  []byte
		want []byte
	}{
		{
			name: "even hex without terminator",
			hex:  []byte{0x01, 0x02, 0x03, 0x04},
			want: []byte{0x12, 0x34},
		},
		{
			name: "even hex with terminator",
			hex:  []byte{0x0a, 0x0b, 16},
			want: []byte{0xab},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HexToKeybytes(tt.hex)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("HexToKeybytes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHexToKeybytesFatalOnOdd(t *testing.T) {
	// log.Fatalf calls os.Exit(1), so we test via subprocess.
	if os.Getenv("TEST_FATAL_ODD_HEX") == "1" {
		HexToKeybytes([]byte{0x01, 0x02, 0x03})
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestHexToKeybytesFatalOnOdd")
	cmd.Env = append(os.Environ(), "TEST_FATAL_ODD_HEX=1")
	err := cmd.Run()
	if e, ok := err.(*exec.ExitError); ok && !e.Success() {
		return // expected: process exited with non-zero status
	}
	t.Fatalf("HexToKeybytes did not exit on odd-length hex")
}

func TestHasTerm(t *testing.T) {
	tests := []struct {
		name string
		s    []byte
		want bool
	}{
		{
			name: "with terminator",
			s:    []byte{0x01, 0x02, 16},
			want: true,
		},
		{
			name: "without terminator",
			s:    []byte{0x01, 0x02, 0x03},
			want: false,
		},
		{
			name: "empty",
			s:    []byte{},
			want: false,
		},
		{
			name: "only terminator",
			s:    []byte{16},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasTerm(tt.s); got != tt.want {
				t.Errorf("HasTerm() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeybytesToHexRoundTrip(t *testing.T) {
	// KeybytesToHex -> HexToKeybytes should return original
	original := []byte{0xab, 0xcd, 0xef}
	hex := KeybytesToHex(original)
	// Remove the terminator for HexToKeybytes
	recovered := HexToKeybytes(hex)
	if !reflect.DeepEqual(original, recovered) {
		t.Errorf("round trip failed: original=%v recovered=%v", original, recovered)
	}
}
