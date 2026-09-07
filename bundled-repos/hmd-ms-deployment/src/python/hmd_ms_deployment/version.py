import logging
import re
from abc import ABC, abstractmethod
from typing import Callable, List

logger = logging.getLogger(__name__)

_NUMERIC_VERSION_RE = re.compile(r"^\d+\.\d+\.\d+$")


class VersionSpecifierException(Exception):
    def __init__(self, spec_type, spec, version):
        self.spec_type = spec_type
        self.spec = spec
        self.version = version

    def __str__(self):
        return f"Incompatible version: Spec: {self.spec_type}{self.spec}, version: {self.version}"

    pass


class AbstractVersionSpecEvaluator(ABC):
    @abstractmethod
    def validate(self, version: str):
        """Validate that the provided version adheres to the configuration
        of the version evaluator.

        :param version: The version number to evaluate
        :type version: str
        :raises: VersionSpecifierException
        """
        pass


class Compatible(AbstractVersionSpecEvaluator):
    def __init__(self, spec: str):
        self.spec = spec
        self.int_spec = spec.split(".")
        for sp in self.int_spec:
            assert (
                sp.isnumeric()
            ), f"Compatible version spec invalid: {spec}. All components must be numeric."
        self.int_spec = [int(sp) for sp in self.int_spec]
        assert (
            len(self.int_spec) > 1
        ), f"Compatible version spec invalid: {spec}. Must be more than 1 component."

    def validate(self, version: str):
        int_version = [int(cmp) for cmp in version.split(".")]
        for n, cmp in enumerate(self.int_spec[:-1]):
            if cmp != int_version[n]:
                raise VersionSpecifierException("~=", self.spec, version)
        if self.int_spec[-1] > int_version[len(self.int_spec) - 1]:
            raise VersionSpecifierException("~=", self.spec, version)


class MatchExclude(AbstractVersionSpecEvaluator):
    def __init__(self, spec: str, matched: bool = True):
        self.spec = spec
        self.matched = matched
        self.symbol = "==" if self.matched else "!="
        self.int_spec = spec.split(".")
        for sp in self.int_spec[:-1]:
            assert (
                sp.isnumeric()
            ), f"Compatible version spec invalid: {spec}. All components except the last must be numeric."
        assert (
            self.int_spec[-1].isnumeric() or self.int_spec[-1] == "*"
        ), f'Compatible version spec invalid: {spec}. The last component must be either numeric or "*".'

        self.int_spec = [int(sp) if sp.isnumeric() else sp for sp in self.int_spec]
        assert (
            len(self.int_spec) > 1
        ), f"Compatible version spec invalid: {spec}. Must be more than 1 component."

    def validate(self, version: str):
        int_version = [int(cmp) for cmp in version.split(".")]
        result = True
        for n, cmp in enumerate(self.int_spec[:-1]):
            if cmp != int_version[n]:
                result = False
        if result and isinstance(self.int_spec[-1], int):
            if self.int_spec[-1] != int_version[len(self.int_spec) - 1]:
                result = False
            if len(int_version) > len(self.int_spec):
                result = False

        if self.matched:
            if not result:
                raise VersionSpecifierException(self.symbol, self.spec, version)
        else:
            if result:
                raise VersionSpecifierException(self.symbol, self.spec, version)


class Ordered(AbstractVersionSpecEvaluator):
    def __init__(self, spec: str, inclusive: bool = True, greater_than: bool = True):
        self.spec = spec
        self.inclusive = inclusive
        self.greater_than = greater_than
        if self.greater_than:
            self.symbol = ">"
        else:
            self.symbol = "<"
        if self.inclusive:
            self.symbol += "="
        self.int_spec = spec.split(".")
        assert (
            len(self.int_spec) == 3
        ), f"Specifier must include three components: {spec}"
        for sp in self.int_spec:
            assert (
                sp.isnumeric()
            ), f"Compatible version spec invalid: {spec}. All components must be numeric."
        self.int_spec = [int(sp) for sp in self.int_spec]

    def operator(self, spec_cmp, ver_cmp):
        if self.greater_than:
            if ver_cmp > spec_cmp:
                return 1
            elif ver_cmp == spec_cmp:
                return 0
            else:
                return -1
        else:
            if ver_cmp < spec_cmp:
                return 1
            elif ver_cmp == spec_cmp:
                return 0
            else:
                return -1

    def validate(self, version: str):
        int_version = [int(cmp) for cmp in version.split(".")]
        for n, cmp in enumerate(self.int_spec):
            compare = self.operator(cmp, int_version[n])
            if compare == -1:
                raise VersionSpecifierException(self.symbol, self.spec, version)
            elif not self.inclusive and compare == 0 and n == len(int_version) - 1:
                raise VersionSpecifierException(self.symbol, self.spec, version)


class VersionSpecifier:
    def __init__(self, version_spec: str):
        version_spec = version_spec.replace(" ", "")
        version_spec = version_spec.split(",")
        self.specs = []  # type: List[AbstractVersionSpecEvaluator]
        for spec in version_spec:
            spec_type = None
            if spec[:2] in ["~=", "==", ">=", "<=", "!="]:
                spec_type = spec[:2]
                spec_config = spec[2:]
            elif spec[:1] in ["<", ">"]:
                spec_type = spec[:1]
                spec_config = spec[1:]
            else:
                assert False, f"Invalid version specifier: {spec}"

            if spec_type == "~=":
                self.specs.append(Compatible(spec_config))
            elif spec_type == "==":
                self.specs.append(MatchExclude(spec_config))
            elif spec_type == "!=":
                self.specs.append(MatchExclude(spec_config, matched=False))
            elif spec_type == ">":
                self.specs.append(
                    Ordered(spec_config, greater_than=True, inclusive=False)
                )
            elif spec_type == ">=":
                self.specs.append(
                    Ordered(spec_config, greater_than=True, inclusive=True)
                )
            elif spec_type == "<":
                self.specs.append(
                    Ordered(spec_config, greater_than=False, inclusive=False)
                )
            elif spec_type == "<=":
                self.specs.append(
                    Ordered(spec_config, greater_than=False, inclusive=True)
                )

    @classmethod
    def validate_version_number(cls, version: str):
        for cmp in version.split("."):
            assert (
                cmp.isnumeric()
            ), f"Invalid version number: {version}. All components must be numeric."

    def validate(self, version: str):
        self.validate_version_number(version)
        for spec in self.specs:
            spec.validate(version)


def sort_major_versions(artifact):
    return int(artifact.split(".")[0])


def sort_minor_versions(artifact):
    return int(artifact.split(".")[1])


def sort_builds(artifact):
    return int(artifact.split(".")[2])


def sort_versions(versions: List, key: Callable):
    sortable = []
    unsortable = []
    for v in versions:
        if v is None:
            logger.warning("sort_versions: skipping None entry")
            continue
        raw = key(v)
        if isinstance(raw, str) and _NUMERIC_VERSION_RE.match(raw):
            sortable.append(v)
        else:
            logger.warning(
                "sort_versions: non-numeric version %r — appending unsorted", raw
            )
            unsortable.append(v)

    sortable.sort(key=lambda v: sort_major_versions(key(v)), reverse=True)
    sortable.sort(key=lambda v: sort_minor_versions(key(v)), reverse=True)
    sortable.sort(key=lambda v: sort_builds(key(v)), reverse=True)

    unsortable.sort(key=lambda v: str(key(v) or ""))

    return sortable + unsortable
