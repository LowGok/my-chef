"""Generate an isolated, credential-free Xcode StoreKit test project."""

import json
from pathlib import Path
import shutil
import sys


root = Path(sys.argv[1]).resolve()
root.mkdir(parents=True, exist_ok=False)
project = root / "StoreKitProbe.xcodeproj"
scheme = project / "xcshareddata" / "xcschemes"
scheme.mkdir(parents=True)
for name in ("Probe.swift", "PurchaseTests.swift"):
    shutil.copyfile(Path(__file__).with_name(name), root / name)

group_id = "452C54C5-E428-4CFF-9446-E1486102069E"
subscriptions = []
for number, (name, period, price) in enumerate([("monthly", "P1M", "6.99"), ("annual", "P1Y", "69.90")], 1):
    title = f"MyChef Premium {name.title()}"
    subscriptions.append({
        "adHocOffers": [], "codeOffers": [], "displayPrice": price, "familyShareable": False,
        "groupNumber": 1, "internalID": str(number), "introductoryOffer": None,
        "localizations": [{"description": title, "displayName": title, "locale": "en_US"}],
        "productID": f"com.gkldevelopment.mychef.preview.premium.{name}",
        "recurringSubscriptionPeriod": period, "referenceName": title,
        "subscriptionGroupID": group_id, "type": "RecurringSubscription", "winBackOffers": [],
    })
catalog = {
    "identifier": "6C8E9661-1F66-4285-A886-013DEF81315B", "nonRenewingSubscriptions": [], "products": [],
    "settings": {"_failTransactionsEnabled": False, "_locale": "en_US", "_storefront": "BEL", "_storeKitErrors": []},
    "subscriptionGroups": [{"id": group_id, "localizations": [], "name": "MyChef Premium", "subscriptions": subscriptions}],
    "version": {"major": 3, "minor": 0},
}
(root / "Premium.storekit").write_text(json.dumps(catalog, indent=2) + "\n")

objects = {}


def entry(isa, **fields):
    key = f"{len(objects) + 1:024X}"
    objects[key] = {"isa": isa, **fields}
    return key


def file(name, type_, *, built=False):
    fields = {"explicitFileType" if built else "lastKnownFileType": type_}
    return entry("PBXFileReference", path=name, sourceTree="BUILT_PRODUCTS_DIR" if built else "<group>", **fields)


def phase(name, *files):
    return entry(name, buildActionMask=2147483647, runOnlyForDeploymentPostprocessing=0,
                 files=[entry("PBXBuildFile", fileRef=ref) for ref in files])


def config(**settings):
    debug = entry("XCBuildConfiguration", name="Debug", buildSettings=settings)
    return entry("XCConfigurationList", buildConfigurations=[debug],
                 defaultConfigurationIsVisible=0, defaultConfigurationName="Debug")


def target(name, product, ref, source, resources=(), dependencies=(), **settings):
    return entry("PBXNativeTarget", name=name, productName=name, productReference=ref,
                 productType=f"com.apple.product-type.{product}", dependencies=list(dependencies), buildRules=[],
                 buildPhases=[phase("PBXSourcesBuildPhase", source), phase("PBXFrameworksBuildPhase"),
                              phase("PBXResourcesBuildPhase", *resources)],
                 buildConfigurationList=config(PRODUCT_NAME=name, GENERATE_INFOPLIST_FILE="YES", **settings))


app_out = file("StoreKitProbe.app", "wrapper.application", built=True)
test_out = file("PurchaseTests.xctest", "wrapper.cfbundle", built=True)
products = entry("PBXGroup", children=[app_out, test_out], name="Products", sourceTree="<group>")
app_file = file("Probe.swift", "sourcecode.swift")
test_file = file("PurchaseTests.swift", "sourcecode.swift")
catalog_file = file("Premium.storekit", "text.json")
main = entry("PBXGroup", children=[app_file, test_file, catalog_file, products], sourceTree="<group>")
app = target("StoreKitProbe", "application", app_out, app_file,
             PRODUCT_BUNDLE_IDENTIFIER="com.gkldevelopment.mychef.storekitprobe",
             INFOPLIST_KEY_UILaunchScreen_Generation="YES")
host = "$(BUILT_PRODUCTS_DIR)/StoreKitProbe.app/StoreKitProbe"
dep = entry("PBXTargetDependency", target=app)
tests = target("PurchaseTests", "bundle.unit-test", test_out, test_file, (catalog_file,), (dep,),
               PRODUCT_BUNDLE_IDENTIFIER="com.gkldevelopment.mychef.storekitprobe.tests",
               TEST_HOST=host, BUNDLE_LOADER=host)
project_id = entry(
    "PBXProject", buildConfigurationList=config(SDKROOT="iphonesimulator", IPHONEOS_DEPLOYMENT_TARGET="27.0",
                                                      SWIFT_VERSION="5.0", CODE_SIGNING_ALLOWED="NO",
                                                      TARGETED_DEVICE_FAMILY="1", SUPPORTED_PLATFORMS="iphonesimulator"),
    compatibilityVersion="Xcode 14.0", developmentRegion="en", knownRegions=["en", "Base"],
    mainGroup=main, productRefGroup=products, projectDirPath="", projectRoot="", targets=[app, tests],
    attributes={"TargetAttributes": {tests: {"TestTargetID": app}}},
)
objects[dep]["targetProxy"] = entry("PBXContainerItemProxy", containerPortal=project_id, proxyType=1,
                                     remoteGlobalIDString=app, remoteInfo="StoreKitProbe")


def openstep(value):
    if isinstance(value, dict):
        return "{\n" + "\n".join(f"{json.dumps(k)} = {openstep(v)};" for k, v in value.items()) + "\n}"
    if isinstance(value, list):
        return "(" + ", ".join(map(openstep, value)) + ")"
    return json.dumps(value)


(project / "project.pbxproj").write_text("// !$*UTF8*$!\n" + openstep(
    {"archiveVersion": 1, "classes": {}, "objectVersion": 56, "objects": objects, "rootObject": project_id}
) + "\n")


def reference(target_id, name, extension):
    return (f'<BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{target_id}" '
            f'BuildableName="{name}.{extension}" BlueprintName="{name}" '
            'ReferencedContainer="container:StoreKitProbe.xcodeproj"/>')


app_ref = reference(app, "StoreKitProbe", "app")
test_ref = reference(tests, "PurchaseTests", "xctest")
(scheme / "StoreKitProbe.xcscheme").write_text(f"""<?xml version="1.0" encoding="UTF-8"?>
<Scheme version="1.7">
  <BuildAction parallelizeBuildables="NO" buildImplicitDependencies="YES">
    <BuildActionEntries>
      <BuildActionEntry buildForTesting="YES" buildForRunning="YES" buildForProfiling="NO" buildForArchiving="NO" buildForAnalyzing="NO">
        {app_ref}
      </BuildActionEntry>
    </BuildActionEntries>
  </BuildAction>
  <TestAction buildConfiguration="Debug" shouldUseLaunchSchemeArgsEnv="YES">
    <Testables><TestableReference skipped="NO" parallelizable="NO">{test_ref}</TestableReference></Testables>
    <MacroExpansion>{app_ref}</MacroExpansion>
  </TestAction>
  <LaunchAction buildConfiguration="Debug"><BuildableProductRunnable>{app_ref}</BuildableProductRunnable></LaunchAction>
</Scheme>
""")
