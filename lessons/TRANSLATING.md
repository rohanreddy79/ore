# Translating lessons

Lessons ship first in: English (en), Hindi (hi), Bengali (bn), Brazilian Portuguese (pt-BR), Indonesian (id), Spanish (es), Arabic (ar, right-to-left), Swahili (sw), Vietnamese (vi), Simplified Chinese (zh-Hans), Japanese (ja). These are the languages of the fastest-growing developer populations plus the largest existing ones (see `research/04` §7).

- Source language is English in `steps/en.md`; translations live beside it as `steps/<lang>.md` with the same headings and code blocks (code is never translated; comments inside code may be).
- Translation happens on Weblate (community, with reviewed suggestions — the Forgejo and GitLab model). A language ships when it is at least 95% translated and reviewed; per-language completion is shown in the product.
- Every shipped translation is run end to end by the lesson pipeline in CI, like the English original, so a mistranslated command fails the build rather than the learner.
- Arabic (and future RTL languages) use the RTL lesson layout; check that code blocks stay left-to-right.
- Prefer plain sentences, keep terms consistent with the glossary (`lessons/GLOSSARY.md`, to be created with the first translations), and never translate product object names that appear in the UI untranslated.
