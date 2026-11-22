#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi
"""Tests for the first-boot setup page (files/argus-core-firstboot.py): the parts that need no VM.

Run: python3 -m unittest discover -s deploy/core-vm/tests
"""
import datetime
import importlib.util
import os
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "files", "argus-core-firstboot.py")


def load():
    spec = importlib.util.spec_from_file_location("argus_core_firstboot", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.dont_write_bytecode = True
    spec.loader.exec_module(mod)
    return mod


FB = load()

WINTER = datetime.datetime(2026, 1, 15, 12, 0, tzinfo=datetime.timezone.utc)
SUMMER = datetime.datetime(2026, 7, 15, 12, 0, tzinfo=datetime.timezone.utc)


class TimezoneListTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)

    def zone(self, name):
        path = os.path.join(self.root, *name.split("/"))
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(b"TZif")

    def zone_tab(self, *names):
        with open(os.path.join(self.root, "zone.tab"), "w", encoding="utf-8") as f:
            f.write("# country\tcoordinates\tTZ\tcomments\n")
            for i, n in enumerate(names):
                f.write("X%d\t+0000+00000\t%s%s\n" % (i, n, "\tsome comment" if i % 2 else ""))

    def test_the_list_is_the_databases_own(self):
        for n in ("Europe/Rome", "Europe/Oslo", "Asia/Kolkata", "America/Argentina/Buenos_Aires", "UTC"):
            self.zone(n)
        # A row whose file is not there is not offered: the form would refuse it.
        self.zone_tab("Europe/Rome", "Europe/Oslo", "Asia/Kolkata", "America/Argentina/Buenos_Aires",
                      "Pacific/Nowhere")
        self.assertEqual(FB.zone_names(self.root),
                         ["America/Argentina/Buenos_Aires", "Asia/Kolkata", "Europe/Oslo", "Europe/Rome"])

    def test_without_zone_tab_every_zone_under_the_areas(self):
        for n in ("Europe/Rome", "America/Indiana/Knox", "Etc/GMT+5", "posix/Europe/Rome", "US/Eastern",
                  "Europe/readme.txt", "UTC"):
            self.zone(n)
        self.assertEqual(FB.zone_names(self.root), ["America/Indiana/Knox", "Europe/Rome"])

    def test_without_a_database_the_short_list(self):
        names = FB.zone_names(os.path.join(self.root, "missing"))
        self.assertIn("Europe/Rome", names)
        self.assertNotIn("UTC", names)
        html = FB.timezone_options("UTC", os.path.join(self.root, "missing"))
        self.assertIn('<option value="UTC" selected>UTC</option>', html)

    def test_options_are_grouped_and_carry_todays_offset(self):
        for n in ("Europe/Rome", "Asia/Kolkata", "America/Argentina/Buenos_Aires", "America/New_York"):
            self.zone(n)
        self.zone_tab("Europe/Rome", "Asia/Kolkata", "America/Argentina/Buenos_Aires", "America/New_York")
        html = FB.timezone_options("Europe/Rome", self.root, WINTER)
        self.assertTrue(html.startswith('<option value="UTC">UTC</option>'))
        self.assertIn('<optgroup label="Europe">', html)
        self.assertIn('<option value="Europe/Rome" selected>Rome (UTC+01:00)</option>', html)
        self.assertIn('<option value="Asia/Kolkata">Kolkata (UTC+05:30)</option>', html)
        self.assertIn('<option value="America/Argentina/Buenos_Aires">Argentina, Buenos Aires (UTC-03:00)</option>', html)
        self.assertIn('<option value="America/New_York">New York (UTC-05:00)</option>', html)
        self.assertEqual(html.count(" selected"), 1)
        # The areas in order, and the places in order inside one.
        self.assertLess(html.index('label="America"'), html.index('label="Asia"'))
        self.assertLess(html.index('label="Asia"'), html.index('label="Europe"'))
        self.assertLess(html.index("Argentina, Buenos Aires"), html.index("New York"))
        # Daylight saving moves the offset the page shows.
        self.assertIn(">Rome (UTC+02:00)<", FB.timezone_options("UTC", self.root, SUMMER))

    def test_a_chosen_zone_outside_the_list_keeps_its_row(self):
        self.zone("Europe/Rome")
        self.zone_tab("Europe/Rome")
        html = FB.timezone_options("Asia/Calcutta", self.root, WINTER)
        self.assertTrue(html.startswith('<option value="Asia/Calcutta" selected>Asia/Calcutta</option>'))
        self.assertEqual(html.count(" selected"), 1)

    def test_only_a_real_zone_file_is_a_zone(self):
        self.zone("Europe/Rome")
        self.zone("UTC")
        self.assertTrue(FB.valid_zone("Europe/Rome", self.root))
        self.assertTrue(FB.valid_zone("UTC", self.root))
        for bad in ("", None, "Europe", "Europe/Nowhere", "../Europe/Rome", "Europe/../UTC", "/etc/passwd",
                    "Europe/Rome/", "a/b/c/d", "Europe/Ro me", "zone.tab"):
            self.assertFalse(FB.valid_zone(bad, self.root), bad)


class FormPageTest(unittest.TestCase):
    def setUp(self):
        self.ip = FB.primary_ip
        FB.primary_ip = lambda: "10.0.0.10"
        self.addCleanup(setattr, FB, "primary_ip", self.ip)

    def test_a_fresh_form_starts_on_the_browsers_zone_and_an_answered_one_does_not(self):
        fresh = FB.form_page(csrf="x")
        self.assertIn('<select id="tz" name="timezone" data-auto="1">', fresh)
        self.assertIn("Intl.DateTimeFormat().resolvedOptions().timeZone", fresh)
        self.assertNotIn("{{", fresh)
        answered = FB.form_page(vals={"timezone": "Europe/Rome"}, csrf="x")
        self.assertIn('<select id="tz" name="timezone">', answered)
        self.assertIn('<option value="Europe/Rome" selected>', answered)


if __name__ == "__main__":
    unittest.main()
