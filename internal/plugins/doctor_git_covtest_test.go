package plugins

import "testing"

func TestRenderDoctorFindingsExactReport(t *testing.T) {
	tests := []struct {
		name     string
		findings []DoctorFinding
		want     string
	}{
		{
			name: "empty",
			want: "0 OK, 0 WARN, 0 FAIL\n",
		},
		{
			name: "counts grouping order and remediation",
			findings: []DoctorFinding{
				{Level: LevelOK, Category: "registry", Message: "registry ok"},
				{Level: LevelWarn, Category: "marketplace", Message: "marketplace stale", Remediation: "run refresh"},
				{Level: LevelFail, Category: "marketplace", Message: "marketplace broken"},
				{Level: LevelOK, Category: "component", Message: "component ok"},
				{Level: LevelWarn, Category: "registry", Message: "registry warn"},
			},
			want: "2 OK, 2 WARN, 1 FAIL\n" +
				"\n[registry]\n" +
				"  OK   registry ok\n" +
				"  WARN registry warn\n" +
				"\n[marketplace]\n" +
				"  WARN marketplace stale\n" +
				"       -> run refresh\n" +
				"  FAIL marketplace broken\n" +
				"\n[component]\n" +
				"  OK   component ok\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderDoctorFindings(tc.findings); got != tc.want {
				t.Fatalf("RenderDoctorFindings() = %q, want %q", got, tc.want)
			}
		})
	}
}
