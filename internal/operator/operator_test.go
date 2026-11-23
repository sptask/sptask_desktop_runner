package operator

import (
	"strings"
	"testing"
)

func TestNormalized(t *testing.T) {
	cases := []struct {
		in   Operator
		want Operator
	}{
		{Operator{}, Operator{Name: DefaultName, Color: DefaultColor}},
		{Operator{Name: "  Ayşe  Yılmaz ", Color: "#12abEF"}, Operator{Name: "Ayşe  Yılmaz", Color: "#12abEF"}},
		{Operator{Name: "Ayşe\n<script>", Color: "red"}, Operator{Name: "Ayşe <script>", Color: DefaultColor}},
		{Operator{Name: "\t\n", Color: "#123"}, Operator{Name: DefaultName, Color: DefaultColor}},
	}
	for _, c := range cases {
		if got := c.in.Normalized(); got != c.want {
			t.Errorf("%+v: got %+v, want %+v", c.in, got, c.want)
		}
	}
	long := Operator{Name: strings.Repeat("ş", MaxNameLength+10)}.Normalized()
	if n := len([]rune(long.Name)); n != MaxNameLength {
		t.Errorf("long name kept %d characters", n)
	}
}

func TestFieldName(t *testing.T) {
	for in, want := range map[string]string{"Müşteri adı*": "Müşteri adı", " Not \n": "Not", "Tutar *": "Tutar", "*": ""} {
		if got := FieldName(in); got != want {
			t.Errorf("FieldName(%q) = %q, want %q", in, got, want)
		}
	}
}
