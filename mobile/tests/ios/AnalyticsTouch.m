#import "AnalyticsTouch.h"
#import <XCTest/XCTest.h>
#import <UIKit/UIKit.h>
#import <TargetConditionals.h>
#include <math.h>

// XCTest has no public multifinger drag API. These test-only selectors match
// Appium WebDriverAgent's native XCTest event declarations at 3d6a4e03cdd673e540289c610f7e189f8ffea434:
// https://github.com/appium/WebDriverAgent/tree/3d6a4e03cdd673e540289c610f7e189f8ffea434/PrivateHeaders/XCTest
// Only declarations are shared; no Appium runtime or downloaded dependency is used.
@interface NSObject (AnalyticsTestTouchSelectors)
- (id)initForTouchAtPoint:(CGPoint)point offset:(double)offset;
- (void)moveToPoint:(CGPoint)point atOffset:(double)offset;
- (void)liftUpAtOffset:(double)offset;
- (id)initWithName:(NSString *)name interfaceOrientation:(long long)orientation;
- (void)addPointerEventPath:(id)path;
- (id)eventSynthesizer;
- (void)synthesizeEvent:(id)record completion:(void (^)(BOOL, NSError *))completion;
@end

NSInteger AnalyticsVoiceOverScroll(CGRect viewport, BOOL upward) {
#if TARGET_OS_SIMULATOR
    if (CGRectIsNull(viewport) || CGRectIsInfinite(viewport)
        || !isfinite(CGRectGetMinX(viewport)) || !isfinite(CGRectGetMinY(viewport))
        || !isfinite(CGRectGetMaxX(viewport)) || !isfinite(CGRectGetMaxY(viewport))
        || viewport.size.width < 200 || viewport.size.height <= viewport.size.width) {
        return 2;
    }
    @try {
        Class paths = NSClassFromString(@"XCPointerEventPath");
        Class records = NSClassFromString(@"XCSynthesizedEventRecord");
        if (!paths || !records
            || ![paths instancesRespondToSelector:@selector(initForTouchAtPoint:offset:)]
            || ![paths instancesRespondToSelector:@selector(moveToPoint:atOffset:)]
            || ![paths instancesRespondToSelector:@selector(liftUpAtOffset:)]
            || ![records instancesRespondToSelector:@selector(initWithName:interfaceOrientation:)]
            || ![records instancesRespondToSelector:@selector(addPointerEventPath:)]
            || ![XCUIDevice.sharedDevice respondsToSelector:@selector(eventSynthesizer)]) {
            return 1;
        }
        id synthesizer = [XCUIDevice.sharedDevice eventSynthesizer];
        if (![synthesizer respondsToSelector:@selector(synthesizeEvent:completion:)]) return 1;
        id record = [[records alloc] initWithName:@"Analytics VoiceOver scroll"
                            interfaceOrientation:UIInterfaceOrientationPortrait];
        if (!record) return 1;
        // Apple VoiceOver scrolls a page with three simultaneous fingers.
        // All paths stay inside the measured app window, away from its edges.
        for (NSInteger finger = 0; finger < 3; finger++) {
            CGFloat x = CGRectGetMidX(viewport) + (finger - 1) * 28;
            CGFloat start = CGRectGetMinY(viewport) + viewport.size.height * (upward ? 0.7 : 0.3);
            CGFloat end = CGRectGetMinY(viewport) + viewport.size.height * (upward ? 0.3 : 0.7);
            id path = [[paths alloc] initForTouchAtPoint:CGPointMake(x, start) offset:0];
            if (!path) return 1;
            [path moveToPoint:CGPointMake(x, end) atOffset:0.45];
            [path liftUpAtOffset:0.5];
            [record addPointerEventPath:path];
        }
        NSLock *lock = [NSLock new];
        __block BOOL finished = NO;
        __block BOOL accepted = NO;
        [synthesizer synthesizeEvent:record completion:^(BOOL result, NSError *error) {
            [lock lock];
            accepted = result && error == nil;
            finished = YES;
            [lock unlock];
        }];
        NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:15];
        while (deadline.timeIntervalSinceNow > 0) {
            [lock lock];
            BOOL done = finished, passed = accepted;
            [lock unlock];
            if (done) return passed ? 0 : 3;
            [NSRunLoop.currentRunLoop runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
        }
        return 4;
    } @catch (NSException *exception) {
        // No private exception message, arbitrary speech or app data is logged.
        return 5;
    }
#else
    return 1;
#endif
}
