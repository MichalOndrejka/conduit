from pathlib import Path
from urllib.parse import quote
from fastapi.templating import Jinja2Templates

_TEMPLATES_DIR = Path(__file__).parent / "templates"
templates = Jinja2Templates(directory=str(_TEMPLATES_DIR))
templates.env.filters["urlenc"] = lambda s: quote(str(s), safe="")
