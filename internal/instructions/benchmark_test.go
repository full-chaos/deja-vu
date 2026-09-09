package instructions

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkResolve(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := snapshot()
			c := ctx()
			c.Task = "current"
			for i := 0; i < n; i++ {
				r := rule(fmt.Sprintf("rule-%d", i))
				r.Scope.Task = fmt.Sprintf("other-%d", i)
				s.Rules = append(s.Rules, r)
			}
			s.Rules[0].Scope.Task = "current"
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := Resolve(s, c)
				if err != nil || len(r.Applicable) != 1 {
					b.Fatal(r, err)
				}
			}
		})
	}
}
func BenchmarkLoadResolveRender(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := snapshot()
			c := ctx()
			c.Task = "current"
			for i := 0; i < n; i++ {
				r := rule(fmt.Sprintf("rule-%d", i))
				r.Scope.Task = fmt.Sprintf("other-%d", i)
				s.Rules = append(s.Rules, r)
			}
			s.Rules[0].Scope.Task = "current"
			store := FileStore{Path: filepath.Join(b.TempDir(), "registry.json")}
			if _, err := store.Replace(0, s); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s, err := store.Load()
				if err != nil {
					b.Fatal(err)
				}
				r, err := Resolve(s, c)
				if err != nil {
					b.Fatal(err)
				}
				if _, err = Render(r, DefaultBudget); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
