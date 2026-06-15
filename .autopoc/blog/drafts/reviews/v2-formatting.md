# Formatting Review -- v2

**Reviewer**: Formatting (Editorial Compliance)
**Draft**: v2.md
**Date**: 2026-06-15

## Scores

| Dimension | Weight | Score | Weighted | Notes |
|---|---|---|---|---|
| Heading hierarchy | 1x | 10 | 10 | Sentence case throughout, no H1 in body, clean H2 cascade |
| Code formatting | 1x | 9 | 9 | No inline backticks, real Dockerfile and YAML, Mermaid diagrams fenced properly |
| CTA placement | 2x | 8 | 16 | Red Hat OpenShift AI linked in para 2 and mid-article; "Try it yourself" closing section with repo + docs links; top CTA is implicit rather than a direct call to action |
| SEO readiness | 1x | 7 | 7 | Keywords present in title and first paragraph; title is 79 characters, exceeding the 50-60 char guideline |
| Link strategy | 1x | 9 | 9 | Four links to redhat.com properties, one to GitHub org repo, no competitor links |
| Editorial compliance | 2x | 7 | 14 | Oxford commas correct, acronyms expanded, numerals used; contractions underused throughout |
| Brand standards | 1x | 9 | 9 | Red Hat brand colors (#EE0000) in Mermaid diagrams and image prompts; product names correct |
| Word count | 1x | 8 | 8 | ~1295 words total (including code/image blocks); at the top end of the 800-1300 tutorial range |
| **Total** | | | **82/100** | |

**Normalized score: 8.2 / 10**

## Line-level feedback

### Contractions (editorial compliance -- most significant issue)

The rubric requires aggressive use of contractions. Multiple instances need fixing:

- **Line 5**: "who is calling" -> "who's calling"
- **Line 29**: "There is no database server to run" -> "There's no database server to run"
- **Line 33**: "What you do not have out of the box is" -> "What you don't have out of the box is"
- **Line 35**: "there is no way to attribute costs" -> "there's no way to attribute costs"
- **Line 82**: "No C libraries are needed" -> "No C libraries are needed" (acceptable as-is, declarative)
- **Line 147**: "there is almost nothing to configure" -> "there's almost nothing to configure"
- **Line 149**: "Go 1.25 requirement is the biggest friction point" -> fine as-is
- **Line 151**: "works out of the box" -> fine
- **Line 153**: "No inference engine is required" -> fine as-is (declarative sentence)

### SEO / Title length

- **Line 3**: Title is 79 characters. Consider shortening: "Deploying a self-hosted LLM gateway on Red Hat OpenShift with Flock" (68 chars) or "Running Flock as a self-hosted LLM gateway on Red Hat OpenShift" (63 chars). Ideally aim for 50-60 characters for search snippet display.

### CTA placement

- **Line 7**: The early mention of Red Hat OpenShift AI is informational, not a direct CTA. Consider adding an explicit CTA sentence near the end of the intro, for example: "If you're managing shared LLM infrastructure, read on to see how Flock can help -- or jump straight to the [Try it yourself](#try-it-yourself) section to deploy it now."
- **Lines 155-166**: Closing CTA is strong with repo link and documentation link. Good.

### Minor observations

- **Line 47**: Mermaid caption uses italics correctly. Good.
- **Line 60**: Mermaid caption uses italics correctly. Good.
- **Line 82**: "CGO_ENABLED=0" appears in running prose. This is acceptable since backticks are prohibited, but consider rephrasing to "Setting CGO_ENABLED to 0 produces a fully static binary..." for readability.
- **Line 84**: "pushed the resulting image to Quay.io in about 3 minutes" -- "Quay.io" is fine (it is the product name, not an inline code reference).
- **Line 134**: "Flock started up in seconds, auto-generated an admin API key, and began serving on port 8080." -- Oxford comma present. Good.

## Editorial compliance checklist

| Rule | Status | Notes |
|---|---|---|
| Sentence case headings | Pass | All 6 headings correct |
| Oxford commas | Pass | Consistently applied |
| No backticks | Pass | Zero inline backticks |
| Full product name on first mention | Pass | "Red Hat OpenShift AI" (line 7), "Red Hat Universal Base Image (UBI)" (line 51), "PersistentVolumeClaim (PVC)" (line 88) |
| Lowercase component descriptors | Pass | "admin dashboard", "inference backends" |
| No H1 in body | Pass | Title uses H2 |
| Expand acronyms on first use | Pass | LLM (line 5), API (line 5), UI (line 29), UBI (line 51), PVC (line 88) |
| Use contractions aggressively | **Fail** | At least 5 instances of "is not", "do not", "there is" that should be contracted |
| Numerals in running text | Pass | "4 test scenarios", "3 inference servers", "8 development teams" |
| No em dashes | Pass | None found |

## Summary

The v2 draft is well-formatted overall. Heading hierarchy, code formatting, link strategy, and brand standards are all strong. The two areas needing attention are:

1. **Contractions** (impacts editorial compliance, weighted 2x): Convert "there is", "do not", "who is" to contracted forms throughout. This is the single highest-impact fix.
2. **Title length** (impacts SEO readiness): Shorten from 79 to 50-60 characters while preserving keywords.

A minor improvement would be adding an explicit, linked CTA near the top of the article rather than relying on the informational Red Hat OpenShift AI mention to serve as the early CTA.

Addressing these items would bring the score to approximately 9.0-9.2.
