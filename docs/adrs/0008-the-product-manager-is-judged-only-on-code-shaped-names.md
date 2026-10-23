# ADR 0008: The Product Manager is judged only on names that could not be ordinary English

- Status: accepted
- Date: 2026-10-23

## Context

`PMDoc.Validate` rejects a Product Manager draft that names a file path or a repository symbol. The
symbol set came from `SymbolNames`: every symbol name and qualified name in the repository graph of
four characters or more. The four-character floor was the only defence against ordinary words, and
its comment said so — "length >= 4, to avoid matching ordinary words".

Four characters does not separate the two. Repositories declare `article`, `entry`, `load`, `start`
and `connected`, and a Product Manager writing about a reading product cannot avoid the word
*article*.

A run against a real 922-file repository failed four times in a row. Each failure named words, not
code:

```text
pm output invalid: epic E2 references technical detail "article"; story S1 references
technical detail "entry"; story S2 references technical detail "connected"; story S5
references technical detail "article"
```

The draft, its one repair and the whole cycle were thrown away each time. The repair could not
succeed either: it is handed the same product input, and the problem list asks it to stop using words
the product is about.

Measured on that repository: 18275 symbol names of four characters or more, of which **237 are plain
lowercase English words**. 165 of the 237 are declared in non-test files, among them `user`,
`search`, `request`, `result`, `status`, `report`, `data`, `message`, `item`, `list`, `link`, `text`
and `query`. Those are not edge cases. They are the vocabulary of every product sentence.

### The guard asked for knowledge the avatar is forbidden to have

This is the deeper fault. INV-3 and ADR 0005 keep the Product Manager on product input only: nine
product keys, no repository graph, enforced by `TestPMInputHasExactlyTheProductFields`. `PMPreamble`
tells it "You do not have knowledge of the codebase" and forbids three concrete things — file paths,
programming languages, database schemas.

Validation then measured the draft against the repository's identifier list. The avatar was failed
for breaking a rule it was never told and could not have checked. Any output it produced was a
coin flip against a word list it cannot see.

## Decision

**A symbol name counts as a technical reference only when it is code-shaped.** `SymbolNames` keeps a
name of four characters or more that carries at least one of:

- `_` or `.` — `load_article_cache`, `billing.Invoice`
- a digit — `h2Title`
- a capital after the first character — `ListInvoices`, `InvoiceTable`

A single run of lowercase letters is dropped, and so is a single capitalised word: `article` and
`Article` are what a sentence looks like at its start, `ArticleRepository` is not. The file-path
pattern is unchanged and still enforces the prompt's first prohibition on its own.

The rule is the prompt's rule, restated so a machine can check it. A Product Manager writing product
English cannot produce `load_article_cache` by accident, so a code-shaped match is evidence. A bare
word is not evidence of anything.

## Consequences

Measured on the same repository, before and after:

| | names in the set | plain English words in it |
| --- | --- | --- |
| before | 18275 | 237 |
| after | 17855 | 0 |

The guard keeps 97.7% of its names. Of the 421 it drops, about 38 are lowercase technical tokens a
Product Manager has no business writing — `redis`, `healthz`, `hgetall`, `fetchall`, `mogrify` and
other client call names. They are 0.2% of the set, they are only reachable if the user's own PRD puts
them there, and their capitalised forms (`Redis`) were never matched anyway. The remaining dropped
names are English plurals the system word list happens not to carry: `records`, `requests`,
`reviews`, `titles`, `warnings`, `permissions`, `inputs`.

`ListInvoices` and `internal/billing/invoice.go` are still rejected;
`TestPMMustNotReferenceTechnicalDetail` and `TestPMReferencingRepositorySymbolsIsRejected` are
unchanged and still pass.

## Alternatives rejected

**Exclude test files.** `impact.IsTestPath` already does this for the impact map, and three of the
five words in the failing run (`article`, `connected`, `start`) came only from test files. It is not
a fix: `entry`, `load`, `user`, `search`, `request` and `status` are declared in non-test files, so
165 ordinary words survive it.

**Match against an English word list.** It is the most direct statement of the intent and it keeps
the `redis` catch. It needs a word list shipped in the binary to be portable, and the list's gaps
become the same bug: a 1000-word list does not carry `article`, so the run that prompted this ADR
would still fail. A rule that is wrong in a way nobody can see is worse than a rule that catches
less.

**Tell the Product Manager the forbidden words.** The only way to make the old check fair, and it
breaks INV-3: the symbol list is repository knowledge, and sending it would put technical material in
the product payload that ADR 0005 exists to keep out.
