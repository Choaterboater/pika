"""Smoke test: the import package is present."""

import python_single


def test_package_imports():
    assert python_single.__doc__
