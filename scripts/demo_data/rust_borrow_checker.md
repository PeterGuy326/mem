# Notes on Ownership

The compiler enforces a simple rule at every scope boundary:
each value has exactly one owner, and when the owner goes out
of scope the value is dropped. References come in two flavors:
any number of shared `&T` borrows, or exactly one exclusive
`&mut T` borrow, never both at once.

This discipline eliminates an entire family of memory bugs
without needing a garbage collector. The cost is upfront: the
programmer must explain lifetimes the compiler cannot infer.
The payoff is downstream: data races become type errors, and
use-after-free is structurally impossible.
