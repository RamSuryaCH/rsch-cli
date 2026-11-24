package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d", got)
	}
}

func TestDivide(t *testing.T) {
	if _, err := Divide(1, 0); err != ErrDivideByZero {
		t.Fatalf("Divide(1, 0) error = %v", err)
	}
	if got, _ := Divide(9, 3); got != 3 {
		t.Fatalf("Divide(9, 3) = %d", got)
	}
}

func TestSum(t *testing.T) {
	if got := Sum(1, 2, 3, 4); got != 10 {
		t.Fatalf("Sum = %d", got)
	}
}
