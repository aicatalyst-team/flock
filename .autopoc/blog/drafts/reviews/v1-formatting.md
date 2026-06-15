# Formatting Review -- v1

## Scores

| Dimension | Weight | Score | Weighted | Notes |
|---|---|---|---|---|
| Heading hierarchy | 1x | 9 | 9 | Sentence case throughout, no H1 in body, clean H2 cascade. No H3s used but structure doesn't demand them. |
| Code formatting | 1x | 4 | 4 | Heavy inline backtick usage (~15 instances). Bash code block is real and runnable. Mermaid diagrams render well. |
| CTA placement | 2x | 3 | 6 | CTA only at the end ("Try it yourself"). No CTA near top or mid-article. Links point to GitHub, not redhat.com. |
| SEO readiness | 1x | 8 | 8 | Title is 56 chars with strong keywords ("LLM gateway," "OpenShift," "Flock"). Keywords repeated in first paragraph. |
| Link strategy | 1x | 4 | 4 | Single link to github.com. Zero internal links to redhat.com or developers.redhat.com. |
| Editorial compliance | 2x | 6 | 12 | Oxford commas consistent. Good contraction use. Multiple acronyms never expanded (LLM, API, UBI, CGO). Numeral inconsistency ("four" vs "37"). |
| Brand standards | 1x | 7 | 7 | Mermaid diagrams use Red Hat brand colors. "Red Hat OpenShift AI" correctly used on first mention. |
| Word count | 1x | 7 | 7 | ~780 words. Slightly under the 800-1300 target for tutorials. |
| **Total** | **10x** | | **57** | |

**Normalized score: 5.7 / 10**

## Line-level feedback

- **Line 1:** `## Running a self-hosted LLM gateway on OpenShift with Flock` -- "LLM" not expanded on first use. Should read "large language model (LLM)" somewhere in the title or opening sentence.
- **Line 3:** `authentication, usage tracking, and an admin dashboard` -- Oxford comma present, good.
- **Line 9:** `Ollama, vLLM, llama.cpp, or cloud APIs like Anthropic and OpenAI` -- Competitor product names (Anthropic, OpenAI) appear explicitly. Consider removing or generalizing to "cloud API providers."
- **Line 18:** `no CGO and no native dependencies` -- "CGO" never expanded or explained. Readers outside the Go ecosystem won't know this term.
- **Line 22:** `If you're running vLLM or Ollama on OpenShift AI` -- Good contraction. But "OpenShift AI" should be "Red Hat OpenShift AI" here since the short form hasn't been formally introduced yet (line 5 uses the full name but as "Red Hat OpenShift AI model serving," not as a standalone introduction of the short form).
- **Line 28:** `ubi9/ubi-minimal` -- Inline backtick. Replace with monospace formatting or rephrase.
- **Line 40:** `` **`CGO_ENABLED=0`** `` -- Bold + backtick combination. Remove backticks; use monospace styling instead.
- **Line 41-43:** Multiple backtick-wrapped terms (`` `modernc.org/sqlite` ``, `` `/opt/app-root/catalog/` ``, `` `/opt/app-root/data` ``). All need backticks removed.
- **Line 48:** `Deployment, one Service, and one PVC` -- "PVC" expanded later as "PersistentVolumeClaim" in the diagram, but should be expanded on first textual use here.
- **Line 61:** `We ran four test scenarios` -- Rubric requires numerals in running text: "We ran 4 test scenarios."
- **Line 66:** `Returns "ok" in 30ms` -- Good numeral use.
- **Line 66:** `Full HTML dashboard with Tailwind CSS` -- "Tailwind CSS" is a competitor/third-party framework name. Not a blocker, but consider whether it adds value.
- **Line 72:** `The single-binary, pure-Go architecture` -- Good descriptor style.
- **Line 84:** `[aicatalyst-team/flock](https://github.com/aicatalyst-team/flock)` -- Only link in the entire post. Need links to redhat.com/openshift-ai, developers.redhat.com, or similar Red Hat properties.
- **Line 94-96:** Bash code block is clean, runnable, and properly formatted. Good.
- **Line 98:** `FLOCK_OLLAMA_ENDPOINT` -- Backtick (implicit in rendering). Final line ends without linking to any Red Hat resource.

## Editorial compliance checklist

| Rule | Status | Details |
|---|---|---|
| Sentence case headings | Pass | All 6 headings use sentence case correctly. |
| Oxford commas | Pass | Consistent throughout (lines 3, 9, 16, 48). |
| No backticks | **Fail** | ~15 inline backtick instances across the draft. |
| Full product name on first mention | Partial | "Red Hat OpenShift AI" used on line 5. "UBI" never expanded. "LLM" never expanded. "API" never expanded. |
| Lowercase component descriptors | Pass | No issues found. |
| No H1 in body | Pass | All headings are H2. |
| Expand acronyms on first use | **Fail** | LLM, API, UBI, CGO, PVC (first textual use), CSS, YAML all unexpanded. |
| Use contractions | Pass | Good contraction use ("you're," "don't," "There's," "What you don't have," "it's"). |
| Numerals in running text | **Fail** | "four test scenarios" (line 61) should be "4 test scenarios." |
| No em dashes | Pass | No em dashes found. |

## Summary

The draft scores well on heading structure, SEO readiness, and contraction use, but has 3 significant compliance failures that pull the score down:

1. **Backticks (highest priority):** ~15 inline backtick instances violate the "no backticks" rule. All technical terms wrapped in backticks need to be reformatted using monospace styling or rephrased as plain text.

2. **CTA placement and linking (highest impact due to 2x weight):** The call-to-action appears only at the bottom and links to GitHub. Add a CTA near the top (after the intro) linking to Red Hat OpenShift AI documentation, and a mid-article CTA linking to developers.redhat.com resources. The closing CTA should also include a redhat.com link alongside the GitHub repo.

3. **Acronym expansion:** LLM, API, UBI, and CGO all need expansion on first use. This is a quick fix with high editorial impact.

Secondary fixes: change "four test scenarios" to "4 test scenarios," consider removing or generalizing competitor names (Anthropic, OpenAI), and add ~50-100 words to bring word count into the 800-1300 target range.
