---
builtin: true
name: prompt-optimize
description: "Load each time you write or fix text that a model reads as instructions: prompts, system prompt lines, tool and skill descriptions, skills, AGENTS.md, lessons, briefs."
---

# Optimize a prompt

Goal: the fewest words that get the behaviour. Every line must change what the model does.

1. If the text is clear and works, keep it. Do not add rules.
2. Put the rule where the model decides. Say what to do and why.
3. Remove repeats, conflicts and lines that change nothing. Use simple words.
4. Test all text you changed. Do not ship it untested.

## Test

Pick the cheapest model of the latest generation from `fir --list-models` (a small "haiku", "flash" or "mini" tier). A cheap model fails where a strong one guesses right, so it shows weak wording.

Write 2–4 variants. Run each on 3+ probes:
- a case that must trigger the behaviour,
- an edge case,
- a negative case that must NOT trigger it.

Test the text where it will live. `--system-prompt` replaces the whole default prompt, so for a skill description or system prompt line, keep the default prompt and put the probe in the task:

```bash
fir -p --model <cheap-model> "<probe task>"                       # default prompt, tools on
fir -p --no-tools --model <cheap-model> --system-prompt "$(cat v2.txt)" "<probe>"  # standalone prompt
```

For a trigger (a description or a "load X when" line), check that the model reads the skill on the positive probe and not on the negative one.

Keep the shortest variant that passes all probes. Report: words before → after, model, probes passed.
