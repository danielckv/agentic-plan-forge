import os
from pathlib import Path


def build(name, count=1):
    return os.path.join(name, str(count))


def _private():
    return 1


class Builder:
    def run(self, target):
        return build(target)

    def _hidden(self):
        return None


@app.get("/items")
def list_items(limit):
    return []
