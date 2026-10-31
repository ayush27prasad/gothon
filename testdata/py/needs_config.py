import os

from pydantic import BaseModel

import gothon


class Settings(BaseModel):
    """Stands in for a pydantic-settings BaseSettings read from the environment."""

    api_key: str


settings = Settings.model_validate({k.removeprefix("NEEDS_CONFIG_").lower(): v for k, v in os.environ.items() if k.startswith("NEEDS_CONFIG_")})


@gothon.task
def key_length() -> int:
    return len(settings.api_key)
