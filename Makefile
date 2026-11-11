bootstrap:
	python3 scripts/rewrite.py bootstrap
build:
	python3 scripts/rewrite.py build
test:
	python3 scripts/rewrite.py test
	python3 scripts/rewrite.py lint
