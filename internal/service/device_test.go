package service

import (
	"reflect"
	"testing"
)

func TestMaxContiguous(t *testing.T) {
	cases := []struct {
		name string
		seqs []int64
		want int64
	}{
		{"empty", []int64{}, 0},
		{"contiguous from 1", []int64{1, 2, 3, 4, 5}, 5},
		{"gap in the middle", []int64{1, 2, 3, 7, 8, 9}, 3},
		{"starts with a gap", []int64{2, 3, 4}, 0},
		{"single first", []int64{1}, 1},
		{"single missing first", []int64{5}, 0},
		{"unsorted input", []int64{3, 1, 2}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxContiguous(tc.seqs); got != tc.want {
				t.Errorf("maxContiguous(%v) = %d, want %d", tc.seqs, got, tc.want)
			}
		})
	}
}

func TestMissingSeqs(t *testing.T) {
	cases := []struct {
		name string
		seqs []int64
		upTo int64
		want []int64
	}{
		{"empty ledger", []int64{}, 3, []int64{1, 2, 3}},
		{"no gaps", []int64{1, 2, 3}, 3, []int64{}},
		{"gaps listed one by one", []int64{1, 2, 5, 6, 10}, 10, []int64{3, 4, 7, 8, 9}},
		{"upTo beyond max seq", []int64{1, 2}, 5, []int64{3, 4, 5}},
		{"upTo below max seq ignores tail", []int64{1, 2, 3, 8, 9}, 3, []int64{}},
		{"zero upTo", []int64{}, 0, []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := missingSeqs(tc.seqs, tc.upTo)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("missingSeqs(%v, %d) = %v, want %v", tc.seqs, tc.upTo, got, tc.want)
			}
		})
	}
}

// A sync checkpoint must let a reinstalled / re-aligned device skip exactly
// the confirmed prefix: next_seq = maxContiguous + 1.
func TestCheckpointResume(t *testing.T) {
	seqs := []int64{1, 2, 3, 4, 6, 7} // 5 lost mid-sync, 6-7 landed later
	next := maxContiguous(seqs) + 1
	if next != 5 {
		t.Errorf("next seq = %d, want 5 (resume at the first gap)", next)
	}
	gaps := missingSeqs(seqs, 7)
	if !reflect.DeepEqual(gaps, []int64{5}) {
		t.Errorf("gaps = %v, want [5]", gaps)
	}
}
