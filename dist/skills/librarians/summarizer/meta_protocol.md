# summarizer — meta_protocol

When you reach a Phase-5 draft decision, record it as a
`librarian_meta` entry. Shape:

```json
{
  "type": "librarian_meta",
  "title": "<one-line summary>",
  "body": "<full reasoning>",
  "status": "DRAFT",
  "metadata": {
    "role": "summarizer",
    "instance_id": "<your instance>",
    "related_entries": [...]
  },
  "tags": ["librarian", "summarizer"]
}
```
