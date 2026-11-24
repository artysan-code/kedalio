# Recall evaluation

`core/cmd/kedalio-recall-eval` asks a copy of the memory a file of questions and scores what `memory_recall` answers:
where each expected page came back, and per mode the hit rate, the mean reciprocal rank (MRR), the share of questions
whose best expected page is first, and the noise (returned pages the question lists in `not`). The scoring is
`core/recalleval`, pure functions over made-up data in its tests. Two runs over the same copy, questions and vectors
give the same JSON, byte for byte: nothing in it depends on the clock or the machine.

## What it guarantees

- **The data folder is never opened.** `core.db` (with `core.db-wal` and `core.db-shm` if present) and `kek` are copied
  into a fresh temporary folder, which is opened and deleted at the end. Opening a database writes (the log, the
  migrations); the folder you pass is left byte for byte as it was.
- **The answers are the tool's.** Each question is a `tools/call` of `memory_recall`, sent in process to the connector's
  HTTP handler (no port is opened). So the answer is what production gives for the same query and mode, including the
  fallback the connector adds when recall finds nothing relevant (the search's best three, scored as `nearest` after the
  pages and the related ones), and the vault's masking.
- **The core is opened as `core/instance` opens it** (key from `kek`, the same migrations, the tasks' groups), but no
  background work starts: no embedding lane, no neighbour refresh, no backups. The key and the recovery code are never
  printed; an instance that is locked is refused, the tool never asks for a code.
- **Meaning comes only from a vectors file.** The query's vector is the stored one for the exact query text; any other
  text is an error inside the search, which leaves the meaning lane out for that query and is listed in
  `missing_vectors`. With no `-vectors` the run is words only, and says so in the summary and the JSON (`vectors`).
- **The clock can be pinned.** `-now` pins the clock `memory_recall` uses for questions of time (`Config.Now` of the
  connector); the output says whether it was pinned. Without it, such questions follow the real clock and two runs on
  different days may differ.

## Questions file

```json
{
  "questions": [
    { "q": "who looks after the plants", "expect": ["people/alice.md"], "not": ["notes/example.md"] },
    { "q": "invoices of the studio", "expect": ["people/bob.md"], "mode": "search" }
  ]
}
```

`expect` are the pages the answer should hold, `not` the ones that were noise before. `mode` (`recall` or `search`) is
optional: without it the question runs in every mode asked for with `-mode`; with it, only in that one. Ranks are
counted from 1 over the answer's order: for recall the pages in full, then the related ones, then the nearest; for
search the results. A document of a collection takes its place too, as `doc:<id>`: search's limit counts it, so the
pages after it rank where the tool shows them. A question of time is searched without its time words, so its vector is the one of what remains:
if it is missing, the run lists the text under `missing_vectors`, and you add it to the vectors file by hand.

## Running it

```sh
cd core && go run ./cmd/kedalio-recall-eval \
  -data <folder with core.db and kek> -questions <questions.json> \
  -vectors <vectors.json> -mode both -now 2026-01-01T00:00:00Z -json <out.json>
```

| Flag                 | Meaning                                                                                       |
| -------------------- | --------------------------------------------------------------------------------------------- |
| `-data`              | a folder with `core.db` and `kek`: it is copied, never opened                                 |
| `-questions`         | the questions file                                                                            |
| `-vectors`           | JSON map query text to vector; none: words only                                               |
| `-mode`              | `recall`, `search` or `both` (default)                                                        |
| `-now`               | RFC 3339; pins recall's clock (default: not pinned)                                           |
| `-budget`, `-limit`  | recall's token budget (2000), search's limit (10)                                             |
| `-json`              | write the full result: totals per mode, ranks per question, the meaning lane's notes          |
| `-embed-model`       | the model the stored chunks were embedded with (required with `-vectors` and `-make-vectors`) |
| `-zone`, `-language` | override what the copy stores (a database that stores none is read as English, `Europe/Rome`) |

It prints a short summary (vectors used or not, whether `now` was pinned, the totals per mode) and exits 1 only on an
error, never on a miss.

The embedding model name must be the one the copy's chunks carry: chunks embedded by another model count as not
embedded and the meaning lane is dropped, as in production.

## Owner-only steps: a real memory

Nothing here is in the repository: the data is the owner's, and stays in a folder outside the repository.

1. **Restore a backup copy** into a new folder outside the repository, with `core/cmd/kedalio-restore`. The store's
   credentials and the recovery code (which checks the copy's tag) come from the environment (`BACKUP_S3_ACCESS_KEY`,
   `BACKUP_S3_SECRET_KEY`, `KEDALIO_RECOVERY_CODE`), never from flags:

   ```sh
   cd core && go run ./cmd/kedalio-restore -endpoint <store address> -bucket <bucket> -to <a new folder outside the repository>
   ```

   The copy is encrypted and comes without its key.
2. **Supply the `kek`**: put the instance's key file, named `kek`, in that folder, from where the owner keeps it. It
   must be the key of the same instance, or the run refuses with a plain message.
3. **Make the vectors once.** Questions' vectors come from the embedding model the core uses: the llama.cpp image pinned
   in `deploy/coolify/core.yaml` (service `embed`, with the model and the settings it lists there). Start that image
   with the same command and the model's cache in a folder outside the repository, publish its port on the loopback,
   and:

   ```sh
   cd core && go run ./cmd/kedalio-recall-eval -make-vectors -questions <questions.json> \
     -vectors <vectors.json> -embed-url http://127.0.0.1:<port> -embed-model bge-m3-q8
   ```

   It calls `/v1/embeddings` once per distinct question and writes the file; nothing else ever calls a model. Stop the
   container after. Keep the questions and the vectors file with the data, not in the repository. Make them again only
   when the questions change or the model does.
4. Run the evaluation as above. Compare the JSON of two code versions (a ranking change, a floor) question by question.
