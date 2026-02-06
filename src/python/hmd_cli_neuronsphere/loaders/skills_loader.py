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

        Supports two formats:
        - File-based: skills/{name}.md
        - Directory-based: skills/{name}/SKILL.md

        Returns:
            List of skill metadata dictionaries
        """
        skills = []

        # Find file-based skills (*.md directly in skills/)
        file_based = list(self.default_location.glob("*.md"))

        # Find directory-based skills (*/SKILL.md)
        dir_based = list(self.default_location.glob("*/SKILL.md"))

        skill_files = file_based + dir_based

        for skill_file in sorted(skill_files):
            try:
                with open(skill_file, "r") as f:
                    content = f.read()
                metadata, _ = self._parse_frontmatter(content)

                # Add filename-based name if not in metadata
                if "name" not in metadata:
                    # For directory-based, use parent dir name
                    if skill_file.name == "SKILL.md":
                        metadata["name"] = skill_file.parent.name
                    else:
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

        Supports two formats:
        - File-based: skills/{name}.md
        - Directory-based: skills/{name}/SKILL.md

        Args:
            name: Skill name (filename without .md extension or directory name)

        Returns:
            Path to the skill file

        Raises:
            FileNotFoundError: If skill doesn't exist
        """
        # Try file-based format first: {name}.md
        skill_path = self.default_location / f"{name}.md"
        if skill_path.exists():
            return skill_path

        # Try directory-based format: {name}/SKILL.md
        dir_skill_path = self.default_location / name / "SKILL.md"
        if dir_skill_path.exists():
            return dir_skill_path

        # Try finding by glob pattern (file-based)
        matches = list(self.default_location.glob(f"*{name}*.md"))
        if matches:
            return matches[0]

        # Try finding by glob pattern (directory-based)
        dir_matches = list(self.default_location.glob(f"*{name}*/SKILL.md"))
        if dir_matches:
            return dir_matches[0]

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
