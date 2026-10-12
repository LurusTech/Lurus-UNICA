package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func float(v float64) *float64 { return &v }

func boolRef(v bool) *bool { return &v }

// tierCases mutates a copy of the base ontology one way each. The C cases are
// the list the design names as untouchable — values, ranges, denial removal or
// change, property deletion, closed_world — plus the neighbours a generator
// could reach for if they were not pinned.
func tierCases() []struct {
	name   string
	want   Tier
	mutate func(o *Ontology)
} {
	return []struct {
		name   string
		want   Tier
		mutate func(o *Ontology)
	}{
		{"nothing", TierNone, func(o *Ontology) {}},
		{"version only", TierNone, func(o *Ontology) { o.Version++ }},

		{"add alias", TierAlias, func(o *Ontology) {
			o.Aliases = map[AliasRef][]string{PropertyAlias("退费比例"): {"退多少钱"}}
		}},

		{"add dimension value", TierDimension, func(o *Ontology) {
			d := o.Dimensions["服务阶段"]
			d.Values = append(d.Values, "enrolled")
			o.Dimensions["服务阶段"] = d
		}},
		{"add dimension", TierDimension, func(o *Ontology) {
			o.Dimensions["客户类型"] = Dimension{Label: "客户类型", Values: []string{"本科", "硕士"}}
		}},
		{"add class", TierDimension, func(o *Ontology) {
			o.Classes["冲刺套餐"] = Class{Label: "冲刺套餐", SubclassOf: "服务套餐"}
		}},
		{"add requires", TierDimension, func(o *Ontology) {
			p := o.Properties["退费比例"]
			p.Requires = append(p.Requires, "服务热线")
			o.Properties["退费比例"] = p
		}},

		{"assertion value", TierMissingFact, func(o *Ontology) {
			for i, a := range o.Assertions {
				if _, ok := a.Values["退费比例"]; ok {
					o.Assertions[i].Values["退费比例"] = StringList{"99"}
					return
				}
			}
			panic("fixture has no 退费比例 assertion")
		}},
		{"assertion added", TierMissingFact, func(o *Ontology) {
			o.Assertions = append(o.Assertions, Assertion{Values: map[string]StringList{"雅思最低要求": {"5"}}})
		}},
		{"assertion removed", TierMissingFact, func(o *Ontology) { o.Assertions = o.Assertions[1:] }},
		{"assertion scope", TierMissingFact, func(o *Ontology) {
			for i, a := range o.Assertions {
				if a.Scope["服务阶段"] == "signed" {
					o.Assertions[i].Scope = Scope{"服务阶段": "delivered"}
					return
				}
			}
			panic("fixture has no signed-stage assertion")
		}},
		{"range values", TierMissingFact, func(o *Ontology) {
			p := o.Properties["签证办理周期"]
			p.Range.Values = []string{"2-4"}
			o.Properties["签证办理周期"] = p
		}},
		{"range min", TierMissingFact, func(o *Ontology) {
			p := o.Properties["雅思最低要求"]
			p.Range.Min = float(4)
			o.Properties["雅思最低要求"] = p
		}},
		{"range max", TierMissingFact, func(o *Ontology) {
			p := o.Properties["雅思最低要求"]
			p.Range.Max = nil
			o.Properties["雅思最低要求"] = p
		}},
		{"range type", TierMissingFact, func(o *Ontology) {
			p := o.Properties["退费比例"]
			p.Range.Type = RangeDecimal
			o.Properties["退费比例"] = p
		}},
		{"range unit", TierMissingFact, func(o *Ontology) {
			p := o.Properties["签证办理周期"]
			p.Range.Unit = "month"
			o.Properties["签证办理周期"] = p
		}},
		{"range label", TierMissingFact, func(o *Ontology) {
			p := o.Properties["签证办理周期"]
			p.Range.Labels = map[string]string{"4-8": "一到两个月"}
			o.Properties["签证办理周期"] = p
		}},
		{"denial removed", TierMissingFact, func(o *Ontology) { o.Denials = o.Denials[1:] }},
		{"denial term", TierMissingFact, func(o *Ontology) { o.Denials[0].Term = "包过" }},
		{"denial note", TierMissingFact, func(o *Ontology) { o.Denials[0].Note = "改写的说明" }},
		{"denial added", TierMissingFact, func(o *Ontology) {
			o.Denials = append(o.Denials, Denial{Term: "中介费返还"})
		}},
		{"property removed", TierMissingFact, func(o *Ontology) { delete(o.Properties, "服务热线") }},
		{"property added", TierMissingFact, func(o *Ontology) {
			o.Properties["奖学金"] = Property{Label: "奖学金", Range: Range{Type: RangeMoney}}
		}},
		{"property label", TierMissingFact, func(o *Ontology) {
			p := o.Properties["退费比例"]
			p.Label = "可退金额"
			o.Properties["退费比例"] = p
		}},
		{"property functional", TierMissingFact, func(o *Ontology) {
			p := o.Properties["退费比例"]
			p.Functional = false
			o.Properties["退费比例"] = p
		}},
		// The commitment mark decides what the firewall withholds while the
		// breaker is open; marking or unmarking a property is a person's call.
		{"property commitment marked", TierMissingFact, func(o *Ontology) {
			p := o.Properties["退费比例"]
			p.Commitment = true
			o.Properties["退费比例"] = p
		}},
		{"property domain", TierMissingFact, func(o *Ontology) {
			p := o.Properties["服务费"]
			p.Domain = "全包套餐"
			o.Properties["服务费"] = p
		}},
		{"requires removed", TierMissingFact, func(o *Ontology) {
			for name, p := range o.Properties {
				if len(p.Requires) > 0 {
					p.Requires = nil
					o.Properties[name] = p
					return
				}
			}
			panic("fixture has no requires constraint")
		}},
		{"closed world off", TierMissingFact, func(o *Ontology) { o.ClosedWorld = boolRef(false) }},
		{"class removed", TierMissingFact, func(o *Ontology) {
			delete(o.Classes, "全包套餐")
		}},
		{"class relabelled", TierMissingFact, func(o *Ontology) {
			o.Classes["全包套餐"] = Class{Label: "尊享套餐", SubclassOf: "服务套餐"}
		}},
		{"disjoint dropped", TierMissingFact, func(o *Ontology) { o.Disjoint = nil }},
		{"dimension value removed", TierMissingFact, func(o *Ontology) {
			d := o.Dimensions["服务阶段"]
			d.Values = d.Values[:2]
			o.Dimensions["服务阶段"] = d
		}},
		{"dimension relabelled", TierMissingFact, func(o *Ontology) {
			d := o.Dimensions["服务阶段"]
			d.Labels = map[string]string{"signed": "刚签约", "submitted": "已提交材料", "delivered": "已递交院校"}
			o.Dimensions["服务阶段"] = d
		}},
		{"product line", TierMissingFact, func(o *Ontology) { o.ProductLine = "别的租户" }},
	}
}

// tierBase is the study-abroad example plus one requires constraint, so the
// removal of one can be exercised.
func tierBase(t *testing.T) *Ontology {
	t.Helper()
	o := loadStudyAbroad(t)
	p := o.Properties["服务费"]
	p.Requires = []string{"申请院校数上限"}
	o.Properties["服务费"] = p
	if err := o.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return o
}

func TestClassifyDiff_EveryChangeLandsInItsTier(t *testing.T) {
	base := tierBase(t)
	for _, tc := range tierCases() {
		t.Run(tc.name, func(t *testing.T) {
			cand := cloneOntology(base)
			tc.mutate(cand)
			if got := ClassifyDiff(base, cand); got != tc.want {
				t.Errorf("ClassifyDiff = %q, want %q (changes: %v)", got, tc.want, DiffOntologies(base, cand))
			}
		})
	}
}

// A fact change must not be laundered by bundling it with an alias: the
// classification is the most restricted tier present, not the most common.
func TestClassifyDiff_AliasDoesNotLaunderAFactChange(t *testing.T) {
	base := tierBase(t)
	for _, tc := range tierCases() {
		if tc.want != TierMissingFact {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			cand := cloneOntology(base)
			tc.mutate(cand)
			cand.Aliases = map[AliasRef][]string{PropertyAlias("服务热线"): {"客服电话号码"}}
			if got := ClassifyDiff(base, cand); got != TierMissingFact {
				t.Errorf("fact change bundled with an alias classified %q", got)
			}
		})
	}
}

func TestClassifyDiff_AliasRemovalIsNotTierA(t *testing.T) {
	fresh := findLine(t, loadSeeds(t), "FreshMart")
	with := withAliases(fresh, map[AliasRef][]string{DenyAlias("质保"): {"保修"}})
	if got := ClassifyDiff(with, fresh); got != TierDimension {
		t.Errorf("withdrawing a denial alias narrows the denial scan; classified %q, want B", got)
	}
}

// Unmarking a commitment property narrows what the firewall withholds, so it
// is tier C in that direction too — and the mark must survive the clone the
// classifier compares, or both directions would silently read as no change.
func TestClassifyDiff_CommitmentUnmarkIsTierC(t *testing.T) {
	base := tierBase(t)
	p := base.Properties["退费比例"]
	p.Commitment = true
	base.Properties["退费比例"] = p
	if !cloneOntology(base).Properties["退费比例"].Commitment {
		t.Fatal("the commitment mark did not survive a round trip")
	}

	cand := cloneOntology(base)
	q := cand.Properties["退费比例"]
	q.Commitment = false
	cand.Properties["退费比例"] = q
	if got := ClassifyDiff(base, cand); got != TierMissingFact {
		t.Errorf("unmarking a commitment property classified %q, want C", got)
	}
}

func TestClassifyDiff_Nil(t *testing.T) {
	if ClassifyDiff(nil, nil) != TierNone {
		t.Error("nil to nil is no change")
	}
	if ClassifyDiff(nil, loadStudyAbroad(t)) != TierMissingFact {
		t.Error("creating an ontology from nothing is a fact change")
	}
}

func TestApplyAlias_OnlyEverAddsAnAlias(t *testing.T) {
	base := loadStudyAbroad(t)
	before := Render(base)

	out, err := ApplyAlias(base, PropertyAlias("退费比例"), " 能退多少 ")
	if err != nil {
		t.Fatalf("ApplyAlias: %v", err)
	}
	if got := ClassifyDiff(base, out); got != TierAlias {
		t.Errorf("classified %q, want A", got)
	}
	if got := out.Aliases[PropertyAlias("退费比例")]; !reflect.DeepEqual(got, []string{"能退多少"}) {
		t.Errorf("aliases = %v", got)
	}
	if base.Aliases != nil || Render(base) != before {
		t.Error("ApplyAlias mutated its input")
	}

	if _, err := ApplyAlias(out, PropertyAlias("退费比例"), "能退多少"); err == nil {
		t.Error("a duplicate phrase should be refused")
	}
	if _, err := ApplyAlias(base, PropertyAlias("不存在"), "能退多少"); err == nil {
		t.Error("an alias of an undeclared property should be refused")
	}
	if _, err := ApplyAlias(base, PropertyAlias("退费比例"), "已提交材料"); err == nil {
		t.Error("an alias equal to a dimension value's wording should be refused")
	}
}

func TestApplyTierB(t *testing.T) {
	base := loadStudyAbroad(t)

	out, err := ApplyDimensionValue(base, "服务阶段", "enrolled", "已入学")
	if err != nil {
		t.Fatalf("ApplyDimensionValue: %v", err)
	}
	if got := ClassifyDiff(base, out); got != TierDimension {
		t.Errorf("dimension value classified %q", got)
	}
	if _, err := ApplyDimensionValue(base, "服务阶段", "signed", ""); err == nil {
		t.Error("an existing value should be refused")
	}

	out, err = ApplyRequires(base, "退费比例", "服务热线")
	if err != nil {
		t.Fatalf("ApplyRequires: %v", err)
	}
	if got := ClassifyDiff(base, out); got != TierDimension {
		t.Errorf("requires classified %q", got)
	}
	if _, err := ApplyRequires(base, "退费比例", "不存在"); err == nil {
		t.Error("requiring an undeclared property should fail validation")
	}
}

// MissingFact is what tier C produces, and the design forbids it from ever
// carrying a value. Pinning the field set means adding one is a deliberate act
// that has to get past this test and its explanation.
func TestMissingFact_HasNowhereToPutAValue(t *testing.T) {
	typ := reflect.TypeOf(MissingFact{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	want := []string{"Property", "Declared", "Scope", "Evidence", "Reason"}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("MissingFact fields = %v, want %v. A missing-fact ticket must not carry a value: "+
			"facts come from the business through a person", fields, want)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), "value") {
			t.Errorf("MissingFact has a field named %s", f)
		}
	}
}

func TestErrNotTierAliasIsDistinguishable(t *testing.T) {
	err := errors.Join(ErrNotTierAlias)
	if !errors.Is(err, ErrNotTierAlias) {
		t.Error("ErrNotTierAlias must be matchable")
	}
}

// The direction of a change is set where it is found, not read back out of its
// wording: deleting a property whose own name says "新增" is still a removal.
func TestDiffOntologies_OpIgnoresWording(t *testing.T) {
	base := tierBase(t)
	withOdd := cloneOntology(base)
	p := withOdd.Properties["服务费"]
	p.Requires = nil
	withOdd.Properties["新增修改渠道费"] = p
	var got []DiffChange
	for _, c := range DiffOntologies(withOdd, base) {
		if strings.Contains(c.What, "新增修改渠道费") {
			got = append(got, c)
		}
	}
	if len(got) != 1 || got[0].Op != DiffRemoved {
		t.Fatalf("removing 新增修改渠道费: got %+v, want one removal", got)
	}
	for _, c := range DiffOntologies(base, withOdd) {
		if strings.Contains(c.What, "新增修改渠道费") && c.Op != DiffAdded {
			t.Errorf("adding 新增修改渠道费 reported %q", c.Op)
		}
	}
}
