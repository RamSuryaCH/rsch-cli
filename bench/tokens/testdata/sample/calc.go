// Package calc is a tiny sample project for the RSCH token benchmark.
package calc

import "errors"

// ErrDivideByZero is returned by Divide when the divisor is zero.
var ErrDivideByZero = errors.New("divide by zero")

// Add returns a + b.
func Add(a, b int) int { return a + b }

// Subtract returns a - b.
func Subtract(a, b int) int { return a - b }

// Multiply returns a * b.
func Multiply(a, b int) int { return a * b }

// Divide returns a / b, or ErrDivideByZero.
func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrDivideByZero
	}
	return a / b, nil
}

// Sum adds all values.
func Sum(values ...int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}
