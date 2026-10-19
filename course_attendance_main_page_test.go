package main

import (
	"os"
	"strings"
	"testing"
)

func TestMainAttendanceUsesCourseAttendance(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}

	main := string(source)

	expected := `mux.Handle("/admin/attendance", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.courseAttendancePageHandler), "attendance.view")))`

	if !strings.Contains(main, expected) {
		t.Fatal("main attendance URL must render course attendance")
	}

	html := courseAttendancePageHTML

	if !strings.Contains(html, `action="/admin/attendance" class="filters"`) {
		t.Fatal("course selection must stay on main attendance URL")
	}

	if strings.Contains(html, `Select Group`) ||
		strings.Contains(html, `Select session`) ||
		strings.Contains(html, `Monthly Report`) ||
		strings.Contains(html, `Open shared attendance`) {
		t.Fatal("legacy attendance controls must not appear")
	}

	if !strings.Contains(html, `id="student-search"`) {
		t.Fatal("live student search missing")
	}
}
