# presidio_sample.jsonl

185 synthetic records sampled from [microsoft/presidio-research][1] (MIT), whose
`synth_dataset_v2.json` is generated from Faker templates. No real person's data.

[1]: https://github.com/microsoft/presidio-research

Every text carrying a `PHONE_NUMBER` or `IBAN_CODE` span is included, plus a
deterministic control sample of records carrying neither. `entities` lists the
entity types the dataset labels in that text, not what Hydra detects.

Rebuild: take `synth_dataset_v2.json`, keep every record whose spans include
PHONE_NUMBER or IBAN_CODE, add `[::12][:100]` of the records that include
neither, and write `{"text", "entities"}` per line.
