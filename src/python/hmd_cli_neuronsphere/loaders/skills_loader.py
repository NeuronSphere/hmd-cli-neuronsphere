import re
from pathlib import Path
from typing import Dict, List, Tuple

import yaml


DEFAULT_LOCATION = Path(__file__).parent.parent / "skills"


class SkillsLoader:
    """Loader for AI agent skill files with YAML frontmatter metadata."""

    def __init__(self, default_location: str = DEFAULT_LOCATION) -> None:
        self.default_location = Path(default_location)

    def _parse_frontmatter(self, content: str) -> Tuple[Dict, str]:
        """Parse YAML frontmatter from markdown content.

        Returns:
            Tuple of (metadata dict, remaining content)
        """
        # Match YAML frontmatter between --- markers
        pattern = r"^---\s*\n(.*?)\n---\s*\n(.*)$"
        match = re.match(pattern, content, re.DOTALL)

        if match:
            frontmatter_str = match.group(1)
            body = match.group(2)
            try:
                metadata = yaml.safe_load(frontmatter_str) or {}
            except yaml.YAMLError:
                metadata = {}
            return metadata, body
        else:
            return {}, content

    def list_skills(self) -> List[Dict]:
        """List all available skills with their metadata.

        Returns:
            List of skill metadata dictionaries
        """
        skills = []
        skill_files = list(self.default_location.glob("*.md"))

        for skill_file in sorted(skill_files):
            try:
                with open(skill_file, "r") as f:
                    content = f.read()
                metadata, _ = self._parse_frontmatter(content)

                # Add filename-based name if not in metadata
                if "name" not in metadata:
                    metadata["name"] = skill_file.stem

                # Add file path for reference
                metadata["_file"] = str(skill_file)

                skills.append(metadata)
            except Exception:
                # Skip files that can't be read
                continue

        return skills

    def load_skill(self, name: str) -> Tuple[Dict, str]:
        """Load a skill by name.

        Args:
            name: Skill name (filename without .md extension)

        Returns:
            Tuple of (metadata dict, full content including frontmatter)
        """
        skill_path = self.get_skill_path(name)

        with open(skill_path, "r") as f:
            content = f.read()

        metadata, _ = self._parse_frontmatter(content)
        if "name" not in metadata:
            metadata["name"] = name

        return metadata, content

    def get_skill_path(self, name: str) -> Path:
        """Get the path to a skill file.

        Args:
            name: Skill name (filename without .md extension)

        Returns:
            Path to the skill file

        Raises:
            FileNotFoundError: If skill doesn't exist
        """
        # Try exact name first
        skill_path = self.default_location / f"{name}.md"
        if skill_path.exists():
            return skill_path

        # Try finding by glob pattern
        matches = list(self.default_location.glob(f"*{name}*.md"))
        if matches:
            return matches[0]

        raise FileNotFoundError(f"Skill '{name}' not found in {self.default_location}")

    def skill_exists(self, name: str) -> bool:
        """Check if a skill exists.

        Args:
            name: Skill name

        Returns:
            True if skill exists, False otherwise
        """
        try:
            self.get_skill_path(name)
            return True
        except FileNotFoundError:
            return False
