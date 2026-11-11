# Domain docs

Single-context: one `GLOSSARY.md` at the repo root, ADRs in `docs/adr/`.

## Before exploring

- Read `GLOSSARY.md`.
- Read the ADRs that touch the area you work in.

## Vocabulary

Name domain concepts (issue titles, proposals, hypotheses, test names) with the glossary terms. Do not use the synonyms listed under _Avoid_. A missing term is either invented language (reconsider) or a gap (note it for `/domain-modeling`).

## ADR conflicts

Surface a contradiction with an ADR explicitly, never override it silently:

> _Contradicts ADR-0001 (best-effort events), but worth reopening because…_
