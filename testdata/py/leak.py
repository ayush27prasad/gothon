import os

import gothon

_kept = []


@gothon.task
def grow(mb: int) -> int:
    """Keep mb more MiB alive, as a leaking library would, and return the pid."""
    _kept.append(b"\x01" * (mb << 20))  # written, so resident
    return os.getpid()
