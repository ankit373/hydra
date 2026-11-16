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

# redaction_corpus.jsonl

Short made-up support, HR, finance, log and legal texts, written for #1886. Every
name, address, email, number and key is invented: example domains, the RFC 5737
and Ofcom drama ranges, documentation IBANs and test card numbers. `planted`
lists each private value and its kind; kinds with no detector (person name,
street address) are there so recall reports what patterns miss.
