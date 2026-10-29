# Writing the docs

How documentation, content text and user-facing messages are written in this
repository. It applies to everything a person reads: `docs/`, the READMEs,
scenario and fault YAML (descriptions, objectives, hints, remediation), CLI
help and errors, and UI text. The website publishes these files as they are,
so there is no later editing pass to catch anything.

Two gates enforce the mechanical parts in `make docs-check`:
`scripts/em-dash-check.sh` and `scripts/prose-check.sh`. The rest is on the
writer and the reviewer.

## Sound like a person who runs this

Write the way an engineer who has used the lab would explain it to a
colleague: direct, specific, a little informal, never selling.

- **Say what to do, then why.** "Run `labctl init` again. It restarts the
  cluster and keeps your apps." Not "To restart the cluster, you can run...".
- **Be concrete.** A command, a number, a file path or an error message beats
  an adjective. "2 CPUs and 4 GB" beats "modest hardware".
- **Short sentences, plain words.** "Use", not "utilize" or "leverage". "Lets
  you", not "empowers you to". "Works", not "works seamlessly".
- **Address the reader as "you".** Use "we" only for the project's decisions.
- **Admit limits plainly.** "This does not work on kind yet" is more useful
  than silence or a hedge.
- **Mark what is not built.** Anything planned says **Planned**. Never
  describe an intention as if it ships.

## Write about the project, not the occasion

Everything in this repository and its GitHub tracker (docs, content text,
comments, commit messages, issues and pull requests) describes SnowOps Labs
itself: what happens, how to reproduce it, what changes and why the product
needs it. It never says what outside work the writer was doing, or what outside
work a change is for.

- **State the problem in the project's terms.** "On v1.9.0, `scenario verify`
  stays at 1 of 8 checks failing after a clean roll", not "while preparing
  material for X, verify kept failing".
- **Justify a change by what it does for a lab user**, never by an external
  plan, audience, deadline or side project.
- **Keep the reproduction, drop the backstory.** Version, commands, observed
  output and expected output are what a reader needs.

## Punctuation and formatting

- **No em dashes.** Use a comma, a colon, parentheses or a plain " - ". The
  gate rejects the character in every tracked text file.
- **Headings say what the section is.** No emoji, no questions, no
  "Introduction" or "Overview" headings that announce nothing.
- **Bold sparingly**, for the one term a skimming reader must not miss. A
  paragraph where every other phrase is bold has no emphasis.
- **Lists for steps and parallel items, prose for reasoning.** Don't break a
  connected explanation into bullets.
- **Tables for comparisons and lookups** (symptom to fix, flag to meaning).
- **Every command runs as written.** Copy it from a terminal you just used.

## Patterns that make text read as generated

Leave these out. Most are padding; the rest are tells that make readers
distrust the page.

- **Openers and closers.** No "In this guide, we will...", "Let's dive in",
  "In summary", "In conclusion", "Happy hacking". Start with the first useful
  sentence and stop after the last one.
- **Hype words.** Seamless, robust (unless it names a tested property),
  powerful, cutting-edge, game-changer, effortless, comprehensive,
  world-class, supercharge, unlock, elevate, empower.
- **Filler phrases.** "It's worth noting that", "It's important to note",
  "plays a crucial role", "a wide range of", "whether you're a beginner or an
  expert", "in today's fast-paced world", "navigate the complexities".
- **Reflex triplets.** "fast, reliable, and scalable" when only one is true.
  List what the thing actually does.
- **Restating the heading** in the first sentence below it.
- **Hedging stacks.** "This may potentially help in some cases." Say what it
  does, or say you don't know.
- **Decorative emoji** in headings, bullets or commit messages.

`scripts/prose-check.sh` holds the list of phrases the gate rejects. Add a
phrase there when you catch a new one in review.

## Content text

Scenario, fault, path and challenge YAML is read in the CLI, the web UI and
on the website.

- `description`: two or three sentences. What the learner walks into, and
  what they will have to do. Not how the scenario was built.
- `objectives`: each starts with a verb and names something the checks test.
- Hints go from a nudge to near the answer, and never paste the fix.
- `remediation`: the command or the next thing to look at, not "check your
  configuration".

## Error messages

GO-CONVENTIONS has the rule for Go errors: say what went wrong and what to do
next. The same applies to shell scripts and UI messages. "Docker has 2 GB;
the lab needs 4 GB. Resize it with: colima stop && colima start --memory 4"
beats "Insufficient resources".
