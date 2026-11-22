package objective

import "testing"

func finding(id, class, title, cwe, tag, proof string) FindingSignal {
	return FindingSignal{
		ID:           id,
		VulnClass:    class,
		Title:        title,
		CWE:          cwe,
		Tag:          tag,
		ExploitProof: proof,
	}
}

func TestEvaluateNilSpecIsNotStructured(t *testing.T) {
	report := Evaluate(nil, Signal{})
	if report.Structured {
		t.Fatal("nil objective must report structured=false")
	}
	if report.Attainment != 0 {
		t.Fatalf("attainment = %v, want 0", report.Attainment)
	}
}

func TestEvaluateEmptyCriteriaHasNoAttainment(t *testing.T) {
	report := Evaluate(&Spec{Kind: "capability", Statement: "get a shell"}, Signal{})
	if !report.Structured {
		t.Fatal("a spec without criteria is still structured")
	}
	if len(report.Criteria) != 0 {
		t.Fatalf("criteria = %d, want 0", len(report.Criteria))
	}
	if report.Attainment != 0 {
		t.Fatalf("attainment = %v, want 0 (no criteria → nothing to score)", report.Attainment)
	}
}

func TestEvaluateFindingClass(t *testing.T) {
	spec := &Spec{
		Kind:      "finding_class",
		Statement: "find a high severity command injection",
		Criteria: []SpecCriterion{
			{ID: "C1", Kind: KindFindingClass, VulnClass: "command injection", VerifyBy: VerifyVerifier, Required: true},
		},
	}

	t.Run("verified finding is met", func(t *testing.T) {
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "command injection", "OS Command Injection", "CWE-78", TagVerified, ""),
		}}
		report := Evaluate(spec, sig)
		if report.Met != 1 || report.Criteria[0].State != StateMet {
			t.Fatalf("report = %+v, want one met criterion", report)
		}
		if report.Criteria[0].EvidenceFindingID != "f1" {
			t.Fatalf("evidence id = %q, want f1", report.Criteria[0].EvidenceFindingID)
		}
		if report.Attainment != 1 {
			t.Fatalf("attainment = %v, want 1", report.Attainment)
		}
	})

	t.Run("manual-only finding is unmet", func(t *testing.T) {
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "command injection", "OS Command Injection", "CWE-78", TagNeedsManualVerif, ""),
		}}
		report := Evaluate(spec, sig)
		if report.Unmet != 1 || report.Criteria[0].State != StateUnmet {
			t.Fatalf("report = %+v, want one unmet criterion", report)
		}
		if report.Attainment != 0 {
			t.Fatalf("attainment = %v, want 0", report.Attainment)
		}
	})

	t.Run("no candidate is undetermined", func(t *testing.T) {
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "xss", "Reflected XSS", "CWE-79", TagVerified, ""),
		}}
		report := Evaluate(spec, sig)
		if report.Undetermined != 1 || report.Criteria[0].State != StateUndetermined {
			t.Fatalf("report = %+v, want one undetermined criterion", report)
		}
	})

	t.Run("verified beats exploit-proven", func(t *testing.T) {
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "command injection", "OS Command Injection", "CWE-78", TagExploitProven, "proof"),
			finding("f2", "command injection", "Command Injection (dup)", "CWE-78", TagVerified, ""),
		}}
		report := Evaluate(spec, sig)
		if report.Criteria[0].EvidenceFindingID != "f2" {
			t.Fatalf("evidence id = %q, want f2 (verified ranks higher)", report.Criteria[0].EvidenceFindingID)
		}
	})
}

func TestEvaluateCapability(t *testing.T) {
	spec := &Spec{
		Kind:      "capability",
		Statement: "obtain a server shell",
		Criteria: []SpecCriterion{
			{ID: "C1", Kind: KindCapability, Capability: "shell", VerifyBy: VerifyDeterministic, Required: true},
		},
	}

	t.Run("proof-backed finding is met", func(t *testing.T) {
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "rce", "Remote Code Execution", "CWE-94", TagExploitProven, "id; whoami → root"),
		}}
		report := Evaluate(spec, sig)
		if report.Met != 1 || report.Criteria[0].EvidenceFindingID != "f1" {
			t.Fatalf("report = %+v, want one met criterion", report)
		}
	})

	t.Run("verified without proof is unmet", func(t *testing.T) {
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "rce", "Remote Code Execution", "CWE-94", TagVerified, ""),
		}}
		report := Evaluate(spec, sig)
		if report.Unmet != 1 {
			t.Fatalf("report = %+v, want one unmet criterion", report)
		}
	})

	t.Run("no candidate is undetermined", func(t *testing.T) {
		report := Evaluate(spec, Signal{})
		if report.Undetermined != 1 {
			t.Fatalf("report = %+v, want one undetermined criterion", report)
		}
	})

	t.Run("manual verify mode stays pending", func(t *testing.T) {
		manual := &Spec{Criteria: []SpecCriterion{
			{ID: "C1", Kind: KindCapability, Capability: "shell", VerifyBy: VerifyManual},
		}}
		sig := Signal{Findings: []FindingSignal{
			finding("f1", "rce", "Remote Code Execution", "CWE-94", TagExploitProven, "proof"),
		}}
		report := Evaluate(manual, sig)
		if report.ManualPending != 1 || report.Criteria[0].State != StateManualPending {
			t.Fatalf("report = %+v, want one manual_pending criterion", report)
		}
		if report.Attainment != 0 {
			t.Fatalf("attainment = %v, want 0 (manual pending is not scored)", report.Attainment)
		}
	})
}

func TestEvaluateCoverage(t *testing.T) {
	spec := &Spec{Criteria: []SpecCriterion{
		{ID: "C1", Kind: KindCoverage, VerifyBy: VerifyDeterministic},
	}}

	t.Run("complete", func(t *testing.T) {
		report := Evaluate(spec, Signal{CoveragePlanned: 10, CoverageCompleted: 10})
		if report.Met != 1 {
			t.Fatalf("report = %+v, want one met criterion", report)
		}
	})
	t.Run("partial", func(t *testing.T) {
		report := Evaluate(spec, Signal{CoveragePlanned: 10, CoverageCompleted: 4})
		if report.Unmet != 1 {
			t.Fatalf("report = %+v, want one unmet criterion", report)
		}
	})
	t.Run("nothing planned", func(t *testing.T) {
		report := Evaluate(spec, Signal{})
		if report.Undetermined != 1 {
			t.Fatalf("report = %+v, want one undetermined criterion", report)
		}
	})
}

func TestEvaluateCountsAndAttainment(t *testing.T) {
	spec := &Spec{Criteria: []SpecCriterion{
		{ID: "C1", Kind: KindFindingClass, VulnClass: "sql injection", VerifyBy: VerifyVerifier},
		{ID: "C2", Kind: KindFindingClass, VulnClass: "xss", VerifyBy: VerifyVerifier},
		{ID: "C3", Kind: KindCoverage, VerifyBy: VerifyDeterministic},
	}}
	sig := Signal{
		Findings: []FindingSignal{
			finding("f1", "sql injection", "SQL Injection in /item", "CWE-89", TagVerified, ""),
		},
		CoveragePlanned:   5,
		CoverageCompleted: 5,
	}
	report := Evaluate(spec, sig)
	if report.Met != 2 || report.Undetermined != 1 {
		t.Fatalf("report = %+v, want 2 met and 1 undetermined", report)
	}
	// attainment = met / (met + unmet + undetermined) = 2/3
	if report.Attainment < 0.66 || report.Attainment > 0.67 {
		t.Fatalf("attainment = %v, want ~0.667", report.Attainment)
	}
}
