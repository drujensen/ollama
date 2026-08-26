package main

// Tasks scale from a one-line change to edits inside files large enough that
// SEARCH text is not trivially unique, across several files, and where the
// obvious textual change is the wrong one. Every task carries a Reference
// edit -- the original plus exactly the requested change -- so the task
// itself can be validated before any model is blamed for failing it.

var editTasks = []EditTask{
	{
		ID: "constant", Difficulty: "single-line",
		Files: []EditFile{{Name: "config.py", Content: `DEFAULT_TIMEOUT = 30
MAX_RETRIES = 3


def retry_budget():
    return MAX_RETRIES * DEFAULT_TIMEOUT
`}},
		Request: `Change MAX_RETRIES from 3 to 5.`,
		Keep:    `DEFAULT_TIMEOUT = 30`,
		Test: `def check():
    assert MAX_RETRIES == 5, MAX_RETRIES
    assert DEFAULT_TIMEOUT == 30
    assert retry_budget() == 150`,
		Reference: []EditFile{{Name: "config.py", Content: `DEFAULT_TIMEOUT = 30
MAX_RETRIES = 5


def retry_budget():
    return MAX_RETRIES * DEFAULT_TIMEOUT
`}},
	},
	{
		ID: "off-by-one", Difficulty: "single-func",
		Files: []EditFile{{Name: "slicing.py", Content: `def last_n(items, n):
    """Return the last n items."""
    return items[len(items) - n - 1:]


def first_n(items, n):
    """Return the first n items."""
    return items[:n]
`}},
		Request: `last_n is off by one and returns n+1 items. Fix it so it returns exactly the last n items.`,
		Keep: `def first_n(items, n):
    """Return the first n items."""
    return items[:n]`,
		Test: `def check():
    assert last_n([1,2,3,4,5], 2) == [4,5]
    assert last_n([1,2,3], 3) == [1,2,3]
    assert last_n([1,2,3], 0) == []
    assert first_n([1,2,3], 2) == [1,2]`,
		Reference: []EditFile{{Name: "slicing.py", Content: `def last_n(items, n):
    """Return the last n items."""
    return items[len(items) - n:] if n else []


def first_n(items, n):
    """Return the first n items."""
    return items[:n]
`}},
	},
	{
		ID: "add-func", Difficulty: "addition",
		Files: []EditFile{{Name: "stats.py", Content: `def mean(xs):
    return sum(xs) / len(xs)


def total(xs):
    return sum(xs)
`}},
		Request: `Add a function median(xs) returning the median of a non-empty list; for an even count return the average of the two middle values. Do not modify the existing functions.`,
		Keep: `def mean(xs):
    return sum(xs) / len(xs)`,
		Test: `def check():
    assert median([3,1,2]) == 2
    assert median([4,1,3,2]) == 2.5
    assert median([7]) == 7
    assert mean([1,2,3]) == 2
    assert total([1,2,3]) == 6`,
		Reference: []EditFile{{Name: "stats.py", Content: `def mean(xs):
    return sum(xs) / len(xs)


def total(xs):
    return sum(xs)


def median(xs):
    s = sorted(xs)
    n = len(s)
    mid = n // 2
    if n % 2:
        return s[mid]
    return (s[mid - 1] + s[mid]) / 2
`}},
	},
	{
		ID: "nested", Difficulty: "indentation",
		Files: []EditFile{{Name: "walker.py", Content: `def collect(tree):
    out = []
    for node in tree:
        if isinstance(node, list):
            for child in node:
                if child is not None:
                    out.append(child)
        else:
            out.append(node)
    return out
`}},
		Request: `In the innermost loop, skip children that are falsy (0 and the empty string as well as None). Keep everything else the same.`,
		Test: `def check():
    assert collect([[1, None, 2]]) == [1, 2]
    assert collect([[0, 1, ""]]) == [1]
    assert collect([5]) == [5]
    assert collect([[None]]) == []`,
		Reference: []EditFile{{Name: "walker.py", Content: `def collect(tree):
    out = []
    for node in tree:
        if isinstance(node, list):
            for child in node:
                if child:
                    out.append(child)
        else:
            out.append(node)
    return out
`}},
	},
	{
		ID: "near-duplicates", Difficulty: "near-duplicate",
		Files: []EditFile{{Name: "handlers.py", Content: `def handle_alpha(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "alpha"
    return record


def handle_beta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "beta"
    return record


def handle_gamma(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "gamma"
    return record


def handle_delta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "delta"
    return record


def handle_epsilon(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "epsilon"
    return record


def handle_zeta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "zeta"
    return record


def handle_eta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "eta"
    return record


def handle_theta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "theta"
    return record


def handle_iota(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "iota"
    return record


def handle_kappa(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "kappa"
    return record


def handle_lam(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "lam"
    return record


def handle_mu(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "mu"
    return record


def handle_nu(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "nu"
    return record


def handle_xi(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "xi"
    return record


def handle_omicron(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "omicron"
    return record


`}},
		Request: `In handle_delta only, add record["audited"] = True just before the return. Do not change any other handler.`,
		Keep: `def handle_gamma(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "gamma"
    return record`,
		Test: `def check():
    assert handle_delta({"a":1})["audited"] is True
    others = [handle_alpha, handle_beta, handle_gamma, handle_epsilon, handle_zeta, handle_eta, handle_theta, handle_iota, handle_kappa, handle_lam, handle_mu, handle_nu, handle_xi, handle_omicron]
    for f in others:
        assert "audited" not in f({"a":1}), f.__name__
    assert handle_delta(None) is None`,
		Reference: []EditFile{{Name: "handlers.py", Content: `def handle_alpha(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "alpha"
    return record


def handle_beta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "beta"
    return record


def handle_gamma(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "gamma"
    return record


def handle_delta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "delta"
    record["audited"] = True
    return record


def handle_epsilon(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "epsilon"
    return record


def handle_zeta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "zeta"
    return record


def handle_eta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "eta"
    return record


def handle_theta(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "theta"
    return record


def handle_iota(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "iota"
    return record


def handle_kappa(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "kappa"
    return record


def handle_lam(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "lam"
    return record


def handle_mu(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "mu"
    return record


def handle_nu(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "nu"
    return record


def handle_xi(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "xi"
    return record


def handle_omicron(payload):
    if not payload:
        return None
    record = dict(payload)
    record["kind"] = "omicron"
    return record


`}},
	},
	{
		ID: "big-locate", Difficulty: "disambiguation",
		Files: []EditFile{{Name: "settings.py", Content: `SETTING_01 = 1
SETTING_02 = 2
SETTING_03 = 3
SETTING_04 = 4
SETTING_05 = 5
SETTING_06 = 6
SETTING_07 = 7
SETTING_08 = 8
SETTING_09 = 9
SETTING_10 = 10
SETTING_11 = 11
SETTING_12 = 12
SETTING_13 = 13
SETTING_14 = 14
SETTING_15 = 15
SETTING_16 = 16
SETTING_17 = 17
SETTING_18 = 18
SETTING_19 = 19
SETTING_20 = 20
SETTING_21 = 21
SETTING_22 = 22
SETTING_23 = 23
SETTING_24 = 24
SETTING_25 = 25
SETTING_26 = 26
SETTING_27 = 27
SETTING_28 = 28
SETTING_29 = 29
SETTING_30 = 30
SETTING_31 = 31
SETTING_32 = 32
SETTING_33 = 33
SETTING_34 = 34
SETTING_35 = 35
SETTING_36 = 36
SETTING_37 = 37
SETTING_38 = 38
SETTING_39 = 39
SETTING_40 = 40
SETTING_41 = 41
SETTING_42 = 42
SETTING_43 = 43
SETTING_44 = 44
SETTING_45 = 45
SETTING_46 = 46
SETTING_47 = 47
SETTING_48 = 48
SETTING_49 = 49
SETTING_50 = 50
SETTING_51 = 51
SETTING_52 = 52
SETTING_53 = 53
SETTING_54 = 54
SETTING_55 = 55
SETTING_56 = 56
SETTING_57 = 57
SETTING_58 = 58
SETTING_59 = 59
SETTING_60 = 60

CONNECT_TIMEOUT_SECONDS = 30
READ_TIMEOUT_SECONDS = 30
WRITE_TIMEOUT_SECONDS = 30
TIMEOUT_SECONDS = 30
IDLE_TIMEOUT_SECONDS = 30
RETRY_TIMEOUT_SECONDS = 30


def effective_timeout():
    return TIMEOUT_SECONDS
`}},
		Request: `Change TIMEOUT_SECONDS to 90. Leave every other timeout constant at 30.`,
		Keep:    `RETRY_TIMEOUT_SECONDS = 30`,
		Test: `def check():
    assert TIMEOUT_SECONDS == 90
    for v in (CONNECT_TIMEOUT_SECONDS, READ_TIMEOUT_SECONDS, WRITE_TIMEOUT_SECONDS, IDLE_TIMEOUT_SECONDS, RETRY_TIMEOUT_SECONDS):
        assert v == 30, v
    assert effective_timeout() == 90`,
		Reference: []EditFile{{Name: "settings.py", Content: `SETTING_01 = 1
SETTING_02 = 2
SETTING_03 = 3
SETTING_04 = 4
SETTING_05 = 5
SETTING_06 = 6
SETTING_07 = 7
SETTING_08 = 8
SETTING_09 = 9
SETTING_10 = 10
SETTING_11 = 11
SETTING_12 = 12
SETTING_13 = 13
SETTING_14 = 14
SETTING_15 = 15
SETTING_16 = 16
SETTING_17 = 17
SETTING_18 = 18
SETTING_19 = 19
SETTING_20 = 20
SETTING_21 = 21
SETTING_22 = 22
SETTING_23 = 23
SETTING_24 = 24
SETTING_25 = 25
SETTING_26 = 26
SETTING_27 = 27
SETTING_28 = 28
SETTING_29 = 29
SETTING_30 = 30
SETTING_31 = 31
SETTING_32 = 32
SETTING_33 = 33
SETTING_34 = 34
SETTING_35 = 35
SETTING_36 = 36
SETTING_37 = 37
SETTING_38 = 38
SETTING_39 = 39
SETTING_40 = 40
SETTING_41 = 41
SETTING_42 = 42
SETTING_43 = 43
SETTING_44 = 44
SETTING_45 = 45
SETTING_46 = 46
SETTING_47 = 47
SETTING_48 = 48
SETTING_49 = 49
SETTING_50 = 50
SETTING_51 = 51
SETTING_52 = 52
SETTING_53 = 53
SETTING_54 = 54
SETTING_55 = 55
SETTING_56 = 56
SETTING_57 = 57
SETTING_58 = 58
SETTING_59 = 59
SETTING_60 = 60

CONNECT_TIMEOUT_SECONDS = 30
READ_TIMEOUT_SECONDS = 30
WRITE_TIMEOUT_SECONDS = 30
TIMEOUT_SECONDS = 90
IDLE_TIMEOUT_SECONDS = 30
RETRY_TIMEOUT_SECONDS = 30


def effective_timeout():
    return TIMEOUT_SECONDS
`}},
	},
	{
		ID: "distant-hunks", Difficulty: "multi-hunk",
		Files: []EditFile{{Name: "pipeline.py", Content: `START_VALUE = 1


def step_01(x):
    return x + 1


def step_02(x):
    return x + 2


def step_03(x):
    return x + 3


def step_04(x):
    return x + 4


def step_05(x):
    return x + 5


def step_06(x):
    return x + 6


def step_07(x):
    return x + 7


def step_08(x):
    return x + 8


def step_09(x):
    return x + 9


def step_10(x):
    return x + 10


def step_11(x):
    return x + 11


def step_12(x):
    return x + 12


def step_13(x):
    return x + 13


def step_14(x):
    return x + 14


def step_15(x):
    return x + 15


def step_16(x):
    return x + 16


def step_17(x):
    return x + 17


def step_18(x):
    return x + 18


def step_19(x):
    return x + 19


def step_20(x):
    return x + 20


def step_21(x):
    return x + 21


def step_22(x):
    return x + 22


def step_23(x):
    return x + 23


def step_24(x):
    return x + 24


def step_25(x):
    return x + 25


def step_26(x):
    return x + 26


def step_27(x):
    return x + 27


def step_28(x):
    return x + 28


def step_29(x):
    return x + 29


def step_30(x):
    return x + 30


def run(x):
    return x


END_VALUE = 2
`}},
		Request: `Change START_VALUE to 10 and END_VALUE to 20.`,
		Keep: `def step_13(x):
    return x + 13`,
		Test: `def check():
    assert START_VALUE == 10
    assert END_VALUE == 20
    assert step_13(0) == 13`,
		Reference: []EditFile{{Name: "pipeline.py", Content: `START_VALUE = 10


def step_01(x):
    return x + 1


def step_02(x):
    return x + 2


def step_03(x):
    return x + 3


def step_04(x):
    return x + 4


def step_05(x):
    return x + 5


def step_06(x):
    return x + 6


def step_07(x):
    return x + 7


def step_08(x):
    return x + 8


def step_09(x):
    return x + 9


def step_10(x):
    return x + 10


def step_11(x):
    return x + 11


def step_12(x):
    return x + 12


def step_13(x):
    return x + 13


def step_14(x):
    return x + 14


def step_15(x):
    return x + 15


def step_16(x):
    return x + 16


def step_17(x):
    return x + 17


def step_18(x):
    return x + 18


def step_19(x):
    return x + 19


def step_20(x):
    return x + 20


def step_21(x):
    return x + 21


def step_22(x):
    return x + 22


def step_23(x):
    return x + 23


def step_24(x):
    return x + 24


def step_25(x):
    return x + 25


def step_26(x):
    return x + 26


def step_27(x):
    return x + 27


def step_28(x):
    return x + 28


def step_29(x):
    return x + 29


def step_30(x):
    return x + 30


def run(x):
    return x


END_VALUE = 20
`}},
	},
	{
		ID: "multi-file", Difficulty: "cross-file",
		Files: []EditFile{{Name: "units.py", Content: `FACTOR = 2


def scale(x):
    return x * FACTOR
`}, {Name: "report.py", Content: `from units import scale


def describe(x):
    return "scaled=%d" % scale(x)
`}},
		Request: `Change FACTOR in units.py to 3, and in report.py change the prefix from "scaled=" to "value=".`,
		Test: `def check():
    assert scale(4) == 12
    assert describe(4) == "value=12", describe(4)`,
		Reference: []EditFile{{Name: "units.py", Content: `FACTOR = 3


def scale(x):
    return x * FACTOR
`}, {Name: "report.py", Content: `from units import scale


def describe(x):
    return "value=%d" % scale(x)
`}},
	},
	{
		ID: "rename-trap", Difficulty: "judgement",
		Files: []EditFile{{Name: "validation.py", Content: `def validate(value):
    """Validate a value. Callers should not call validate directly."""
    if value is None:
        raise ValueError("validate received None")
    return True


def run(value):
    return validate(value)


DOC = "call validate before use"
`}},
		Request: `Rename the function validate to check_input and update its call sites. Do not change any string literal or docstring text.`,
		Test: `def check():
    assert check_input(1) is True
    assert run(1) is True
    assert DOC == "call validate before use", DOC
    try:
        check_input(None)
    except ValueError as e:
        assert "validate received None" in str(e), str(e)
    else:
        raise AssertionError("expected ValueError")
    assert "validate" not in globals()`,
		Reference: []EditFile{{Name: "validation.py", Content: `def check_input(value):
    """Validate a value. Callers should not call validate directly."""
    if value is None:
        raise ValueError("validate received None")
    return True


def run(value):
    return check_input(value)


DOC = "call validate before use"
`}},
	},
	{
		ID: "subtle-bug", Difficulty: "reasoning",
		Files: []EditFile{{Name: "searching.py", Content: `HELP = 'binary search helpers'


def filler_01(x):
    return x


def filler_02(x):
    return x


def filler_03(x):
    return x


def filler_04(x):
    return x


def filler_05(x):
    return x


def filler_06(x):
    return x


def filler_07(x):
    return x


def filler_08(x):
    return x


def filler_09(x):
    return x


def filler_10(x):
    return x


def filler_11(x):
    return x


def filler_12(x):
    return x


def filler_13(x):
    return x


def filler_14(x):
    return x


def filler_15(x):
    return x


def filler_16(x):
    return x


def filler_17(x):
    return x


def filler_18(x):
    return x


def filler_19(x):
    return x


def filler_20(x):
    return x


def filler_21(x):
    return x


def filler_22(x):
    return x


def filler_23(x):
    return x


def filler_24(x):
    return x


def filler_25(x):
    return x


def bsearch(items, target):
    lo, hi = 0, len(items)
    while lo < hi:
        mid = (lo + hi) // 2
        if items[mid] == target:
            return mid
        if items[mid] < target:
            lo = mid
        else:
            hi = mid
    return -1
`}},
		Request: `bsearch hangs on some inputs because the low bound never advances. Fix it. Do not change the function signature or the return convention.`,
		Keep: `def filler_07(x):
    return x`,
		Test: `def check():
    xs = list(range(0, 200, 2))
    for t in xs:
        assert xs[bsearch(xs, t)] == t, t
    assert bsearch(xs, 199) == -1
    assert bsearch([], 1) == -1`,
		Reference: []EditFile{{Name: "searching.py", Content: `HELP = 'binary search helpers'


def filler_01(x):
    return x


def filler_02(x):
    return x


def filler_03(x):
    return x


def filler_04(x):
    return x


def filler_05(x):
    return x


def filler_06(x):
    return x


def filler_07(x):
    return x


def filler_08(x):
    return x


def filler_09(x):
    return x


def filler_10(x):
    return x


def filler_11(x):
    return x


def filler_12(x):
    return x


def filler_13(x):
    return x


def filler_14(x):
    return x


def filler_15(x):
    return x


def filler_16(x):
    return x


def filler_17(x):
    return x


def filler_18(x):
    return x


def filler_19(x):
    return x


def filler_20(x):
    return x


def filler_21(x):
    return x


def filler_22(x):
    return x


def filler_23(x):
    return x


def filler_24(x):
    return x


def filler_25(x):
    return x


def bsearch(items, target):
    lo, hi = 0, len(items)
    while lo < hi:
        mid = (lo + hi) // 2
        if items[mid] == target:
            return mid
        if items[mid] < target:
            lo = mid + 1
        else:
            hi = mid
    return -1
`}},
	},
}
