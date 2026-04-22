package node

import (
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
		// TODO: Add test cases.
		{
			name: "Test 1",
			args: args{
				str: common.FromHex("1111"),
			},
			want: common.FromHex("0101010110"),
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
		// TODO: Add test cases.
		{
			name: "Test 1",
			args: args{
				a: common.FromHex("01010302"),
				b: common.FromHex("01010202"),
			},
			want: 2,
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
