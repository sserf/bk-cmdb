/*
 * Tencent is pleased to support the open source community by making 蓝鲸 available.,
 * Copyright (C) 2017-2018 THL A29 Limited, a Tencent company. All rights reserved.
 * Licensed under the MIT License (the ",License",); you may not use this file except
 * in compliance with the License. You may obtain a copy of the License at
 * http://opensource.org/licenses/MIT
 * Unless required by applicable law or agreed to in writing, software distributed under
 * the License is distributed on an ",AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
 * either express or implied. See the License for the specific language governing permissions and
 * limitations under the License.
 */

package extensions

import (
	"context"
	"fmt"
	"net/http"

	"configcenter/src/auth/meta"
	"configcenter/src/auth/parser"
	"configcenter/src/common"
	"configcenter/src/common/blog"
	"configcenter/src/common/condition"
	"configcenter/src/common/mapstr"
	"configcenter/src/common/metadata"
	"configcenter/src/common/util"
)

func (am *AuthManager) CollectAuditCategoryByBusinessID(ctx context.Context, header http.Header, businessID int64) ([]AuditCategorySimplify, error) {

	query := &metadata.QueryInput{
		Condition: condition.CreateCondition().Field(common.BKAppIDField).Eq(businessID).ToMapStr(),
	}
	response, err := am.clientSet.AuditController().GetAuditLog(context.Background(), header, query)
	if nil != err {
		blog.Errorf("collect audit category by business %d failed, get audit log failed, err: %+v", businessID, err)
		return nil, fmt.Errorf("collect audit category by business %d failed, get audit log failed, err: %+v", businessID, err)
	}
	/*
		response, err := am.clientSet.CoreService().Instance().ReadInstance(context.Background(), header, common.BKTableNameOperationLog, query)
		if err != nil {
			blog.Errorf("get audit log by business %d failed, err: %+v", businessID, err)
			return nil, fmt.Errorf("get audit log by business %d failed, err: %+v", businessID, err)
		}
	*/

	data, err := mapstr.NewFromInterface(response.Data)
	if nil != err {
		blog.Errorf("collect audit category by business %d failed, parse response data failed, data: %+v, error info is %+v", businessID, response.Data, err)
		return nil, fmt.Errorf("collect audit category by business %d failed, parse response data failed, error info is %+v", businessID, err)
	}
	auditLogs, err := data.MapStrArray("info")
	if nil != err {
		blog.Errorf("collect audit category by business %d failed, extract audit log from response data failed, data: %+v, error info is %+v", businessID, response.Data, err)
		return nil, fmt.Errorf("collect audit category by business %d failed, extract audit log from response data failed, error info is %+v", businessID, err)
	}

	categories := make([]AuditCategorySimplify, 0)
	modelIDs := make([]string, 0)
	modelIDFound := map[string]bool{}
	for _, item := range auditLogs {
		category := &AuditCategorySimplify{}
		category, err := category.Parse(item)
		if err != nil {
			blog.Errorf("parse audit category simplify failed, category: %+v, err: %+v", category, err)
			continue
		}
		if _, exist := modelIDFound[category.BKOpTargetField]; exist == false {
			modelIDs = append(modelIDs, category.BKOpTargetField)
			categories = append(categories, *category)
			modelIDFound[category.BKOpTargetField] = true
		}
	}
	blog.V(5).Infof("audit log are belong to model: %+v", modelIDs)
	modelIDs = util.StrArrayUnique(modelIDs)
	objects, err := am.collectObjectsByObjectIDs(ctx, header, modelIDs...)
	if err != nil {
		blog.Errorf("collectObjectsByObjectIDs failed, model: %+v, err: %+v", modelIDs, err)
		return nil, fmt.Errorf("get audit category related models failed, err: %+v", err)
	}
	objectIDMap := map[string]int64{}
	for _, object := range objects {
		objectIDMap[object.ObjectID] = object.ID
	}

	// invalid categories will be filter out
	validCategories := make([]AuditCategorySimplify, 0)
	for _, category := range categories {
		modelID, existed := objectIDMap[category.BKOpTargetField]
		if existed == true {
			category.ModelID = modelID
			validCategories = append(validCategories, category)
		} else {
			blog.Errorf("unexpect audit op_target: %s", category.BKOpTargetField)
		}
	}

	blog.V(4).Infof("list audit categories by business %d result: %+v", businessID, validCategories)
	return validCategories, nil
}

func (am *AuthManager) ExtractBusinessIDFromAuditCategories(categories ...AuditCategorySimplify) (int64, error) {
	var businessID int64
	for idx, category := range categories {
		bizID := category.BKAppIDField
		if idx > 0 && bizID != businessID {
			return 0, fmt.Errorf("get multiple business ID from audit categories")
		}
		businessID = bizID
	}
	return businessID, nil
}

func (am *AuthManager) MakeResourcesByAuditCategories(ctx context.Context, header http.Header, action meta.Action, businessID int64, categories ...AuditCategorySimplify) ([]meta.ResourceAttribute, error) {
	// prepare resource layers for authorization
	resources := make([]meta.ResourceAttribute, 0)
	for _, category := range categories {
		// instance
		resource := meta.ResourceAttribute{
			Basic: meta.Basic{
				Action:     action,
				Type:       meta.AuditLog,
				Name:       category.BKOpTargetField,
				InstanceID: category.ModelID,
			},
			SupplierAccount: util.GetOwnerID(header),
			BusinessID:      businessID,
		}

		resources = append(resources, resource)
	}

	blog.V(9).Infof("MakeResourcesByAuditCategories: %+v", resources)
	return resources, nil
}

func (am *AuthManager) RegisterAuditCategories(ctx context.Context, header http.Header, categories ...AuditCategorySimplify) error {
	if len(categories) == 0 {
		return nil
	}

	businessID, err := am.ExtractBusinessIDFromAuditCategories(categories...)
	if err != nil {
		return fmt.Errorf("extract business id from audit categories failed, err: %+v", err)
	}

	resources, err := am.MakeResourcesByAuditCategories(ctx, header, meta.EmptyAction, businessID, categories...)
	if err != nil {
		return fmt.Errorf("make auth resource by audit categories failed, err: %+v", err)
	}

	if err := am.Authorize.RegisterResource(ctx, resources...); err != nil {
		return fmt.Errorf("register audit categories failed, err: %+v", err)
	}
	return nil
}

// MakeAuthorizedAuditListCondition make a query condition, with which user can only search audit log under it.
// ==> [{"bk_biz_id":2,"op_target":{"$in":["module"]}}]
func (am *AuthManager) MakeAuthorizedAuditListCondition(ctx context.Context, header http.Header, businessID int64) (cond []mapstr.MapStr, hasAuthorization bool, err error) {
	// businessID 0 means audit log priority of special model on any business

	commonInfo, err := parser.ParseCommonInfo(&header)
	if err != nil {
		return nil, false, fmt.Errorf("parse user info from request header failed, %+v", err)
	}

	businessIDs := make([]int64, 0)
	if businessID == 0 {
		ids, err := am.Authorize.GetAuthorizedBusinessList(ctx, commonInfo.User)
		if err != nil {
			blog.Errorf("make condition from authorization failed, get authorized businesses failed, err: %+v", err)
			return nil, false, fmt.Errorf("make condition from authorization failed, get authorized businesses failed, err: %+v", err)
		}
		businessIDs = ids
	}
	businessIDs = append(businessIDs, 0)
	blog.V(5).Infof("audit on business %+v to be check", businessIDs)

	authorizedBusinessModelMap := map[int64][]string{}
	for _, businessID := range businessIDs {
		auditList, err := am.Authorize.GetAuthorizedAuditList(ctx, commonInfo.User, businessID)
		if err != nil {
			blog.Errorf("get authorized audit by business %d failed, err: %+v", businessID, err)
			return nil, false, fmt.Errorf("get authorized audit by business %d failed, err: %+v", businessID, err)
		}
		blog.Infof("get authorized audit by business %d result: %s", businessID, auditList)
		blog.InfoJSON("get authorized audit by business %s result: %s", businessID, auditList)

		modelIDs := make([]int64, 0)
		for _, authorizedList := range auditList {
			for _, resourceID := range authorizedList.ResourceIDs {
				if len(resourceID) == 0 {
					continue
				}
				modelID := resourceID[len(resourceID)-1].ResourceID
				id, err := util.GetInt64ByInterface(modelID)
				if err != nil {
					blog.Errorf("get authorized audit by business %d failed, err: %+v", businessID, err)
					return nil, false, fmt.Errorf("get authorized audit by business %d failed, err: %+v", businessID, err)
				}
				modelIDs = append(modelIDs, id)
			}
		}

		if len(modelIDs) == 0 {
			continue
		}
		objects, err := am.collectObjectsByRawIDs(ctx, header, modelIDs...)
		if err != nil {
			blog.Errorf("get related model with id %+v by authorized audit failed, err: %+v", modelIDs, err)
			return nil, false, fmt.Errorf("get related model with id %+v by authorized audit failed, err: %+v", modelIDs, err)
		}

		objectIDs := make([]string, 0)
		for _, object := range objects {
			objectIDs = append(objectIDs, object.ObjectID)
		}
		authorizedBusinessModelMap[businessID] = objectIDs
	}

	blog.InfoJSON("authorizedBusinessModelMap result: %s", authorizedBusinessModelMap)
	cond = make([]mapstr.MapStr, 0)

	// extract authorization on any business
	if _, ok := authorizedBusinessModelMap[0]; ok == true {
		if len(authorizedBusinessModelMap[0]) > 0 {
			hasAuthorization = true
			item := condition.CreateCondition()
			item.Field(common.BKOpTargetField).In(authorizedBusinessModelMap[0])

			cond = append(cond, item.ToMapStr())
			delete(authorizedBusinessModelMap, 0)
		}
	}

	// extract authorization on special business and object
	for businessID, objectIDs := range authorizedBusinessModelMap {
		hasAuthorization = true
		item := condition.CreateCondition()
		item.Field(common.BKOpTargetField).In(objectIDs)
		item.Field(common.BKAppIDField).Eq(businessID)

		cond = append(cond, item.ToMapStr())
	}

	blog.V(5).Infof("MakeAuthorizedAuditListCondition result: %+v", cond)
	return cond, hasAuthorization, nil
}
