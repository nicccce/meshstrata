package rules

import (
	"context"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/rules/rulespec"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func (s *Service) SaveProfile(ctx context.Context, m domain.Mutation, input stage3.ProfileInput) (control.Pin, []stage3.Issue, error) {
	if err := ctx.Err(); err != nil {
		return control.Pin{}, nil, err
	}
	if err := validMutation(m, input.ID, input.Name); err != nil {
		return control.Pin{}, nil, err
	}
	if len(input.Sets) > rulespec.MaxSets {
		return control.Pin{}, nil, invalid("/sets")
	}
	if len(input.Source) > stage3.MaxRuleSourceBytes || !utf8.ValidString(input.Source) {
		_, issues := rulespec.ParseSource(input.Source)
		return control.Pin{}, issues, invalid("/source")
	}
	// Digest the bounded request, including raw source positions and the
	// expected revision, before any read or mutation of catalog state.
	hash, err := control.ComponentDigest(struct {
		Expected domain.Revision
		Input    stage3.ProfileInput
	}{m.ExpectedRevision, input})
	if err != nil {
		return control.Pin{}, nil, problem(internal)
	}
	if p, err := s.replay(ctx, saveProf, m.IdempotencyKey, hash); err != nil || p.Valid() {
		return p, nil, err
	}
	lines, issues := rulespec.ParseSource(input.Source)
	if err := ctx.Err(); err != nil {
		return control.Pin{}, nil, err
	}
	if len(issues) != 0 {
		return control.Pin{}, issues, invalid("/source")
	}
	pins := make(map[domain.ID]control.Pin, len(input.Sets))
	for i, p := range input.Sets {
		if !p.Valid() || pins[p.ID] != (control.Pin{}) {
			return control.Pin{}, nil, invalid(pathIndex("/sets", i))
		}
		pins[p.ID] = p
	}
	used := make(map[domain.ID]bool, len(pins))
	for i, l := range lines {
		if l.Kind == "RULE-SET" {
			p, ok := pins[domain.ID(l.Value)]
			if !ok {
				issues = append(issues, stage3.Issue{Path: "/source", Line: l.Line, Column: l.Column, Code: domain.NotFound})
				continue
			}
			used[p.ID] = true
			lines[i].DomainSet = &p
		}
	}
	if len(issues) != 0 {
		return control.Pin{}, issues, invalid("/source")
	}
	if len(used) != len(pins) {
		return control.Pin{}, nil, invalid("/sets")
	}
	spec := stage3.ProfileSpec{Rules: lines, Sets: make([]stage3.DomainSetSnapshot, 0, len(pins))}
	if len(pins) != 0 {
		err = s.db.Read(ctx, func(q *dbgen.Queries) error {
			total := 0
			for _, p := range input.Sets {
				d, err := readSet(ctx, q, p)
				if err != nil {
					return err
				}
				for _, domain := range d.Spec.Domains {
					total += len(domain) + 1
				}
				if total > rulespec.MaxSetBytes {
					return invalid("/sets")
				}
				spec.Sets = append(spec.Sets, d)
			}
			return nil
		})
		if err = safeResult(ctx, err); err != nil {
			return control.Pin{}, nil, err
		}
	}
	pin, at, err := s.preparePin(ctx, m, "profile", input.ID, spec)
	if err != nil {
		return control.Pin{}, nil, err
	}
	if !rulespec.ValidateProfile(stage3.ProfileSnapshot{Pin: pin, Spec: spec}) {
		return control.Pin{}, nil, invalid("/sets")
	}
	out, err := s.write(ctx, saveProf, m.IdempotencyKey, hash, pin, func(q *dbgen.Queries) error {
		return saveProfile(ctx, q, m, input, spec, pin, at)
	})
	return out, nil, err
}

func saveProfile(ctx context.Context, q *dbgen.Queries, m domain.Mutation, input stage3.ProfileInput, spec stage3.ProfileSpec, pin control.Pin, at string) error {
	if err := stage3.RequireActive(ctx, q, "profile", pin.ID); err != nil {
		return err
	}
	for _, p := range input.Sets {
		if err := stage3.RequireActive(ctx, q, "domain_set", p.ID); err != nil {
			return err
		}
	}
	id, rev := string(pin.ID), int64(pin.Revision)
	if m.ExpectedRevision == 0 {
		if err := q.B07InsertProfileHead(ctx, dbgen.B07InsertProfileHeadParams{ID: id, Name: input.Name, Revision: rev, UpdatedAt: at}); err != nil {
			return err
		}
	} else {
		changed, err := q.B07UpdateProfileHead(ctx, dbgen.B07UpdateProfileHeadParams{ID: id, Name: input.Name, Revision: rev, UpdatedAt: at, ExpectedRevision: int64(m.ExpectedRevision)})
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict(q.B07GetProfileHead(ctx, id))
		}
	}
	last := spec.Rules[len(spec.Rules)-1]
	if err := q.B07InsertProfileRevision(ctx, dbgen.B07InsertProfileRevisionParams{
		ProfileID: id, Revision: rev, FallbackTarget: rulespec.TargetText(last.Target), SourceText: input.Source,
	}); err != nil {
		return err
	}
	if err := q.B07InsertProfilePin(ctx, dbgen.B07InsertProfilePinParams{
		ProfileID: id, Revision: rev, Digest: pin.Digest, MatchLine: int64(last.Line), MatchColumn: int64(last.Column),
	}); err != nil {
		return err
	}
	for i, d := range spec.Sets {
		p := d.Pin
		set, setRev := string(p.ID), int64(p.Revision)
		got, err := q.B07GetDomainPin(ctx, dbgen.B07GetDomainPinParams{DomainSetID: set, Revision: setRev})
		if err != nil {
			return err
		}
		if got != p.Digest {
			return problem(domain.NotFound)
		}
		if err := q.B07InsertLegacySetRef(ctx, dbgen.B07InsertLegacySetRefParams{ProfileID: id, ProfileRevision: rev, DomainSetID: set, DomainSetRevision: setRev}); err != nil {
			return err
		}
		if err := q.B07InsertProfileSetPin(ctx, dbgen.B07InsertProfileSetPinParams{ProfileID: id, ProfileRevision: rev, DomainSetID: set, DomainSetRevision: setRev, Digest: p.Digest, Position: int64(i)}); err != nil {
			return err
		}
	}
	for i, r := range spec.Rules[:len(spec.Rules)-1] {
		options := ""
		if r.NoResolve {
			options = "no-resolve"
		}
		if err := q.B07InsertRuleLine(ctx, dbgen.B07InsertRuleLineParams{
			ProfileID: id, Revision: rev, Position: int64(i), Type: r.Kind, Value: r.Value,
			Target: rulespec.TargetText(r.Target), Options: options, SourceLine: nullable(r.Line), SourceColumn: nullable(r.Column),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Profile(ctx context.Context, pin control.Pin) (stage3.ProfileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return stage3.ProfileSnapshot{}, err
	}
	if !pin.Valid() {
		return stage3.ProfileSnapshot{}, invalid("/pin")
	}
	var spec stage3.ProfileSpec
	err := s.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		spec, err = readProfile(ctx, q, pin)
		return err
	})
	snap := stage3.ProfileSnapshot{Pin: pin, Spec: spec}
	if err == nil && !rulespec.ValidateProfile(snap) {
		err = problem(domain.NotFound)
	}
	if err = safeResult(ctx, err); err != nil {
		return stage3.ProfileSnapshot{}, err
	}
	return snap, nil
}

func readProfile(ctx context.Context, q *dbgen.Queries, pin control.Pin) (stage3.ProfileSpec, error) {
	var spec stage3.ProfileSpec
	id, rev := string(pin.ID), int64(pin.Revision)
	broken := problem(domain.NotFound)
	head, err := q.B07GetProfilePin(ctx, dbgen.B07GetProfilePinParams{ProfileID: id, Revision: rev})
	if err != nil {
		return spec, err
	}
	if pin.Digest != head.Digest {
		return spec, broken
	}
	refs, err := q.B07ListProfileSetPins(ctx, dbgen.B07ListProfileSetPinsParams{ProfileID: id, ProfileRevision: rev})
	if err != nil {
		return spec, err
	}
	pins := make(map[domain.ID]control.Pin, len(refs))
	spec.Sets = make([]stage3.DomainSetSnapshot, 0, len(refs))
	total := 0
	for i, row := range refs {
		p := control.Pin{ID: domain.ID(row.DomainSetID), Revision: domain.Revision(row.DomainSetRevision), Digest: row.Digest}
		if row.Position != int64(i) || i >= rulespec.MaxSets || !p.Valid() {
			return spec, broken
		}
		d, err := readSet(ctx, q, p)
		if err != nil {
			return spec, err
		}
		for _, d := range d.Spec.Domains {
			total += len(d) + 1
		}
		if total > rulespec.MaxSetBytes {
			return spec, broken
		}
		pins[p.ID] = p
		spec.Sets = append(spec.Sets, d)
	}
	lines, err := q.B07ListRuleLines(ctx, dbgen.B07ListRuleLinesParams{ProfileID: id, Revision: rev})
	if err != nil {
		return spec, err
	}
	spec.Rules = make([]stage3.RuleLine, 0, len(lines)+1)
	for i, row := range lines {
		target, ok := rulespec.ParseTarget(row.Target)
		if !ok || row.Position != int64(i) || !row.SourceLine.Valid || !row.SourceColumn.Valid ||
			row.Options != "" && row.Options != "no-resolve" {
			return spec, broken
		}
		r := stage3.RuleLine{Kind: row.Type, Value: row.Value, Target: target,
			Line: int(row.SourceLine.Int64), Column: int(row.SourceColumn.Int64), NoResolve: row.Options != ""}
		if r.Kind == "RULE-SET" {
			p, ok := pins[domain.ID(r.Value)]
			if !ok {
				return spec, broken
			}
			r.DomainSet = &p
		}
		spec.Rules = append(spec.Rules, r)
	}
	target, ok := rulespec.ParseTarget(head.FallbackTarget)
	if !ok {
		return spec, broken
	}
	spec.Rules = append(spec.Rules, stage3.RuleLine{Kind: "MATCH", Target: target, Line: int(head.MatchLine), Column: int(head.MatchColumn)})
	return spec, nil
}
