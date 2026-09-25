package router

import (
	"errors"
	"fmt"
)

func Apply(r Runner, c Controller, op Op, value bool) (bool, error) {
	if !supports(c, op) {
		return false, ErrUnsupportedOp
	}

	plan, err := c.Plan(r, op, value)
	if err != nil {
		return false, err
	}

	// менять нечего
	if plan.Empty() {
		return c.ReadState(r, op)
	}

	var state bool
	verify := func() error {
		var err error
		if state, err = c.ReadState(r, op); err != nil {
			return err
		}
		if state != value {
			return fmt.Errorf("после применения состояние %v, ожидалось %v", state, value)
		}
		return nil
	}
	err = applyPlan(r, plan, c.Restart, verify)
	return state, err
}

// запись, замена, перезапуск и проверка с откатом
func applyPlan(r Runner, plan *Plan, restart func(Runner) error, verify func() error) error {
	written := make([]FileChange, 0, len(plan.Changes))
	cleanup := func() {
		for _, ch := range written {
			removeFile(r, ch.Path+tmpSuffix)
		}
	}

	for _, ch := range plan.Changes {
		if ch.Validate != nil {
			if err := ch.Validate(ch.Content); err != nil {
				cleanup()
				return fmt.Errorf("%w: %s: %v", ErrUnknownFormat, ch.Path, err)
			}
		}
		if err := writeFile(r, ch.Path+tmpSuffix, ch.Content); err != nil {
			cleanup()
			return err
		}
		written = append(written, ch)

		if ch.RemoteCheck != "" {
			cmd := fmt.Sprintf(ch.RemoteCheck, shq(ch.Path+tmpSuffix))
			if out, err := r.Run(cmd); err != nil {
				cleanup()
				return fmt.Errorf("%w: %s не прошёл проверку: %v (%s)",
					ErrUnknownFormat, ch.Path, err, out)
			}
		}
	}

	replaced := make([]FileChange, 0, len(plan.Changes))
	for _, ch := range plan.Changes {
		if err := replaceFile(r, ch.Path); err != nil {
			rbErr := rollback(r, restart, replaced)
			cleanup()
			if rbErr != nil {
				return fmt.Errorf("%w: %v (причина: %v)", ErrRollbackFailed, rbErr, err)
			}
			return fmt.Errorf("%w: %v", ErrRolledBack, err)
		}
		replaced = append(replaced, ch)
	}

	cause := restart(r)
	if cause == nil {
		cause = verify()
	}
	if cause != nil {
		if rbErr := rollback(r, restart, replaced); rbErr != nil {
			return fmt.Errorf("%w: %v (причина: %v)", ErrRollbackFailed, rbErr, cause)
		}
		return fmt.Errorf("%w: %v", ErrRolledBack, cause)
	}
	return nil
}

// ошибка e-13
func rollback(r Runner, restart func(Runner) error, replaced []FileChange) error {
	if len(replaced) == 0 {
		return nil
	}
	var errs []error
	for _, ch := range replaced {
		if err := restoreFile(r, ch.Path); err != nil {
			errs = append(errs, err)
		}
	}
	if err := restart(r); err != nil {
		errs = append(errs, fmt.Errorf("перезапуск службы после отката: %w", err))
	}
	return errors.Join(errs...)
}

// дифф для показа человеку в ручном режиме
func Diff(p *Plan) []FileDiff {
	if p.Empty() {
		return nil
	}
	out := make([]FileDiff, 0, len(p.Changes))
	for _, ch := range p.Changes {
		out = append(out, FileDiff{
			Path:    ch.Path,
			Removed: lineDiff(ch.Before, ch.Content),
			Added:   lineDiff(ch.Content, ch.Before),
		})
	}
	return out
}

type FileDiff struct {
	Path    string
	Removed []string
	Added   []string
}

func lineDiff(a, b string) []string {
	inB := make(map[string]int)
	for _, l := range splitLines(b) {
		inB[l]++
	}
	var out []string
	for _, l := range splitLines(a) {
		if inB[l] > 0 {
			inB[l]--
			continue
		}
		if trimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}
