#!/usr/bin/env python3
"""Tests for make_seed.py. Run: python3 -B -m pytest tools/prompt-eval/memory-lab/"""
import os, sys, unittest

sys.dont_write_bytecode = True
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import make_seed


class SeedPages(unittest.TestCase):
    def test_no_page_repeats_a_tag(self):
        for filler in (make_seed.FILLER, 999):
            for p in make_seed.seed_pages(filler):
                self.assertEqual(len(p["tags"]), len(set(p["tags"])), p["path"])


if __name__ == "__main__":
    unittest.main()
