package eval

import (
	"slices"
	"sort"
)

// Scores are one question's ranking metrics.
type Scores struct {
	// Hit1 is 1 when the top document is relevant.
	Hit1 float64 `json:"hit_at_1"`
	// RecallAt5 / RecallAtK are the share of relevant documents among the
	// top 5 / top K.
	RecallAt5 float64 `json:"recall_at_5"`
	RecallAtK float64 `json:"recall_at_k"`
	// RR is the reciprocal rank of the first relevant document (0 if none in
	// the top K); its mean over questions is MRR.
	RR float64 `json:"reciprocal_rank"`
	// Empty is 1 when nothing at all came back: the failure an agent can't
	// tell apart from "no such data".
	Empty float64 `json:"empty"`
}

// scoreRanking scores ranked document IDs against the relevant ones.
func scoreRanking(ranked, relevant []string, k int) (firstRank int, s Scores) {
	if len(ranked) == 0 {
		s.Empty = 1
	}
	if len(ranked) > k {
		ranked = ranked[:k]
	}
	var in5, inK int
	for i, id := range ranked {
		if !slices.Contains(relevant, id) {
			continue
		}
		if firstRank == 0 {
			firstRank = i + 1
		}
		inK++
		if i < 5 {
			in5++
		}
	}
	if firstRank == 1 {
		s.Hit1 = 1
	}
	if firstRank > 0 {
		s.RR = 1 / float64(firstRank)
	}
	if n := len(relevant); n > 0 {
		s.RecallAt5 = float64(in5) / float64(n)
		s.RecallAtK = float64(inK) / float64(n)
	}
	return firstRank, s
}

// Aggregate is the mean of Scores over a group of questions.
type Aggregate struct {
	N         int     `json:"n"`
	Hit1      float64 `json:"hit_at_1"`
	RecallAt5 float64 `json:"recall_at_5"`
	RecallAtK float64 `json:"recall_at_k"`
	MRR       float64 `json:"mrr"`
	Empty     float64 `json:"empty_rate"`
	// Agent-level means; zero for retrieval runs.
	AnswerRecall    float64 `json:"answer_recall,omitempty"`
	AnswerPrecision float64 `json:"answer_precision,omitempty"`
	ToolCalls       float64 `json:"tool_calls,omitempty"`
	Errors          int     `json:"errors,omitempty"`
}

func aggregate(results []QueryResult) Aggregate {
	var a Aggregate
	var agentN int
	for _, r := range results {
		if r.Error != "" {
			a.Errors++
		}
		a.N++
		a.Hit1 += r.Scores.Hit1
		a.RecallAt5 += r.Scores.RecallAt5
		a.RecallAtK += r.Scores.RecallAtK
		a.MRR += r.Scores.RR
		a.Empty += r.Scores.Empty
		if r.Agent != nil {
			agentN++
			a.AnswerRecall += r.Agent.AnswerRecall
			a.AnswerPrecision += r.Agent.AnswerPrecision
			a.ToolCalls += float64(r.Agent.ToolCalls)
		}
	}
	if a.N > 0 {
		n := float64(a.N)
		a.Hit1 /= n
		a.RecallAt5 /= n
		a.RecallAtK /= n
		a.MRR /= n
		a.Empty /= n
	}
	if agentN > 0 {
		n := float64(agentN)
		a.AnswerRecall /= n
		a.AnswerPrecision /= n
		a.ToolCalls /= n
	}
	return a
}

// Summary groups results by layout, overall and per intent and company.
type Summary struct {
	Layouts   []string                        `json:"layouts"`
	Overall   map[string]Aggregate            `json:"overall"`    // layout →
	ByIntent  map[string]map[string]Aggregate `json:"by_intent"`  // intent → layout →
	ByCompany map[string]map[string]Aggregate `json:"by_company"` // company → layout →
}

// Summarize aggregates results; layouts keeps the order of first appearance.
func Summarize(results []QueryResult) Summary {
	s := Summary{
		Overall:   map[string]Aggregate{},
		ByIntent:  map[string]map[string]Aggregate{},
		ByCompany: map[string]map[string]Aggregate{},
	}
	byLayout := map[string][]QueryResult{}
	byIntent := map[string]map[string][]QueryResult{}
	byCompany := map[string]map[string][]QueryResult{}
	for _, r := range results {
		if _, ok := byLayout[r.Layout]; !ok {
			s.Layouts = append(s.Layouts, r.Layout)
		}
		byLayout[r.Layout] = append(byLayout[r.Layout], r)
		if byIntent[r.Intent] == nil {
			byIntent[r.Intent] = map[string][]QueryResult{}
		}
		byIntent[r.Intent][r.Layout] = append(byIntent[r.Intent][r.Layout], r)
		if byCompany[r.Company] == nil {
			byCompany[r.Company] = map[string][]QueryResult{}
		}
		byCompany[r.Company][r.Layout] = append(byCompany[r.Company][r.Layout], r)
	}
	for l, rs := range byLayout {
		s.Overall[l] = aggregate(rs)
	}
	for intent, m := range byIntent {
		s.ByIntent[intent] = map[string]Aggregate{}
		for l, rs := range m {
			s.ByIntent[intent][l] = aggregate(rs)
		}
	}
	for c, m := range byCompany {
		s.ByCompany[c] = map[string]Aggregate{}
		for l, rs := range m {
			s.ByCompany[c][l] = aggregate(rs)
		}
	}
	return s
}

// Change is one question whose first relevant rank differs from the
// baseline layout's.
type Change struct {
	Company      string `json:"company"`
	QueryID      string `json:"query_id"`
	Query        string `json:"query"`
	Layout       string `json:"layout"`
	BaselineRank int    `json:"baseline_rank"` // 0 = not retrieved
	Rank         int    `json:"rank"`
	// Above lists the documents ranked above the first relevant one (or the
	// top 3 when none was retrieved): what crowded the answer out.
	Above []string `json:"above,omitempty"`
}

// Changes lists, per non-baseline layout, the questions that got better or
// worse than under baseline, worst regressions first.
func Changes(results []QueryResult, baseline string) []Change {
	base := map[string]QueryResult{}
	for _, r := range results {
		if r.Layout == baseline {
			base[r.Company+"/"+r.QueryID] = r
		}
	}
	var out []Change
	for _, r := range results {
		if r.Layout == baseline {
			continue
		}
		b, ok := base[r.Company+"/"+r.QueryID]
		if !ok || b.FirstRelevantRank == r.FirstRelevantRank {
			continue
		}
		above := r.Ranked
		if r.FirstRelevantRank > 0 {
			above = r.Ranked[:r.FirstRelevantRank-1]
		} else if len(above) > 3 {
			above = above[:3]
		}
		out = append(out, Change{Company: r.Company, QueryID: r.QueryID, Query: r.Query, Layout: r.Layout,
			BaselineRank: b.FirstRelevantRank, Rank: r.FirstRelevantRank, Above: slices.Clone(above)})
	}
	sort.SliceStable(out, func(i, j int) bool { return regression(out[i]) > regression(out[j]) })
	return out
}

// regression is how much worse a change is (negative = improvement); a
// missed answer counts as rank K+1.
func regression(c Change) float64 {
	rr := func(rank int) float64 {
		if rank == 0 {
			return 0
		}
		return 1 / float64(rank)
	}
	return rr(c.BaselineRank) - rr(c.Rank)
}
